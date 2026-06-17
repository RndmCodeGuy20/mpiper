package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/database"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/server"
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
