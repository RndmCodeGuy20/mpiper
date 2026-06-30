package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/database"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/outbox"
	"github.com/rndmcodeguy20/mpiper/internal/queue"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/internal/server"
	"github.com/rndmcodeguy20/mpiper/internal/webhook"
	"github.com/rndmcodeguy20/mpiper/pkg/logger"
	"go.uber.org/zap"
)

var (
	Env        = "development"
	Version    = "1.0.0"
	CommitHash = "abc1234"
	BuildTime  = "2024-06-01T12:00:00Z"
	Author     = "RndmCodeGuy"
)

func main() {
	// --health-check is used by the container HEALTHCHECK. It must be a
	// lightweight probe against the already-running server — NOT a second
	// server boot (which would fail to bind the port). Exit 0 if /healthz is OK.
	for _, arg := range os.Args[1:] {
		if arg == "--health-check" {
			runHealthCheck()
			return
		}
	}

	cfg, err := config.InitializeConfig(config.ToEnvironment(Env))
	if err != nil {
		panic(err)
	}
	config.Init(cfg)

	serverCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	baseLogger := logger.New(logger.Config{
		ServiceName: cfg.Otel.ServiceName,
		Environment: cfg.Environment,
		Level:       logger.ParseLevel(cfg.LogLevel),
		EnableOTel:  true,
	}).With(
		zap.String("version", Version),
		zap.String("commit_hash", CommitHash),
		zap.String("build_time", BuildTime),
		zap.String("author", Author),
	)
	defer func() {
		if err := logger.Sync(baseLogger); err != nil {
			panic(err)
		}
	}()

	baseLogger.Sugar().Infof("Starting %s server on https://%s:%d in %s mode", "MPiper", cfg.Server.Host, cfg.Server.Port, cfg.Environment)

	shutdownTracer := metrics.InitTracer(serverCtx, baseLogger)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownTracer(ctx); err != nil {
			baseLogger.Sugar().Errorf("Failed to shut down tracer: %v", err)
		}
	}()

	m, shutdownMetrics := metrics.InitMetrics(serverCtx, baseLogger)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := shutdownMetrics(ctx); err != nil {
			baseLogger.Sugar().Errorf("Failed to shut down metrics: %v", err)
		}
	}()

	db, err := database.NewPostgresDB(cfg.DB)
	if err != nil {
		baseLogger.Sugar().Fatalf("Failed to connect to database: %v", err)
	}
	defer func(db *sqlx.DB) {
		if err := db.Close(); err != nil {
			baseLogger.Sugar().Errorf("Failed to close database connection: %v", err)
		}
	}(db)

	if cfg.AutoMigrate {
		baseLogger.Info("AUTO_MIGRATE=true: running migrations")
		if err := database.RunMigrations(db.DB); err != nil {
			baseLogger.Sugar().Fatalf("Migration failed: %v", err)
		}
		baseLogger.Info("Migrations applied successfully")
	}

	// --- Outbox relay ---
	rc, err := queue.MustGetRedisClient(&cfg.Redis, baseLogger)
	if err != nil {
		baseLogger.Sugar().Fatalf("Failed to create Redis client: %v", err)
	}
	rq := queue.NewRedisQueue(serverCtx, rc, queue.RedisQueueOptions{
		QueueName:         "media:jobs",
		ConnectionTimeOut: 2 * time.Second,
		MaxStreamLength:   10_000,
		MaxRetries:        3,
		RetryInterval:     2 * time.Second,
		EnableMetrics:     true,
	}, baseLogger, m)

	outboxRepo := repository.NewOutboxRepository(db, baseLogger)
	relay := outbox.NewRelay(outboxRepo, rq, baseLogger, m, cfg.Outbox.RelayInterval, cfg.Outbox.RelayBatch)
	_ = m.RegisterOutboxPendingFunc(func(ctx context.Context) (int64, error) {
		return outboxRepo.CountPending(ctx)
	})

	// Observe the database connection-pool stats (in-use / idle / open / max /
	// wait count). sqlx.DB embeds *sql.DB, so db.Stats() exposes pool saturation
	// — the key signal for whether the DB pool is a bottleneck under load.
	_ = m.RegisterDBStatsFunc(func() sql.DBStats {
		return db.Stats()
	})
	go relay.Start(serverCtx)
	go relay.StartCleanup(serverCtx, cfg.Outbox.Retention)

	// --- Webhook dispatcher ---
	webhookDispatcher := webhook.NewDispatcher(db, baseLogger, webhook.DispatcherConfig{
		PollInterval:  cfg.Webhook.PollInterval,
		BatchSize:     cfg.Webhook.BatchSize,
		Timeout:       cfg.Webhook.Timeout,
		MaxAttempts:   cfg.Webhook.MaxAttempts,
		EncryptionKey: cfg.EncryptionKey,
		Retention:     cfg.Webhook.Retention,
	})
	go webhookDispatcher.Start(serverCtx)
	go webhookDispatcher.StartCleanup(serverCtx)

	_ = m.RegisterWebhookPendingFunc(func(ctx context.Context) (int64, error) {
		var count int64
		err := db.GetContext(ctx, &count, `SELECT COUNT(*) FROM webhook_deliveries WHERE status = 'pending'`)
		return count, err
	})

	srv := server.NewServer(db, cfg, m)
	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			baseLogger.Fatal("Server error", zap.Error(err))
		}
	}()

	<-serverCtx.Done()
	baseLogger.Info("Shutting down server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		baseLogger.Error("shutdown failed", zap.Error(err))
	}
}

// runHealthCheck performs a lightweight HTTP probe against the running server's
// /healthz endpoint and exits 0 (healthy) or 1 (unhealthy). It deliberately
// avoids the full startup path so it can run as a container HEALTHCHECK without
// contending for the listen port.
func runHealthCheck() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "5010"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/healthz", port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "health check failed: status %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}
