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
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

var (
	Env        = "development"
	Version    = "0.1.0"
	CommitHash = "abc1234"
	BuildTime  = "2024-06-01T12:00:00Z"
	Author     = "RndmCodeGuy"
)

func main() {
	cfg, err := config.InitializeConfig(config.ToEnvironment(Env))
	if err != nil {
		panic(err)
	}

	baseLogger := utils.NewLogger().With(
		zap.String("version", Version),
		zap.String("commit_hash", CommitHash),
		zap.String("build_time", BuildTime),
		zap.String("author", Author),
	)
	defer func(l *utils.Logger) {
		err := l.Close()
		if err != nil {
			panic(err)
		}
	}(baseLogger)

	baseLogger.Sugar().Infof("Starting %s server on https://%s:%d in %s mode", "MPiper", cfg.Server.Host, cfg.Server.Port, cfg.Environment)

	tracerCtx := context.Background()
	shutdownTracer := metrics.InitTracer(tracerCtx, *baseLogger)
	defer func() {
		err := shutdownTracer(tracerCtx)
		if err != nil {
			baseLogger.Sugar().Errorf("Failed to shut down tracer: %v", err)
		}
	}()

	// Initialize metrics
	metricsCtx := context.Background()
	shutdownMetrics := metrics.InitMetrics(metricsCtx, *baseLogger)
	defer func() {
		err := shutdownMetrics(metricsCtx)
		if err != nil {
			baseLogger.Sugar().Errorf("Failed to shut down metrics: %v", err)
		}
	}()

	db, err := database.NewPostgresDB(cfg.DB)
	if err != nil {
		baseLogger.Sugar().Fatalf("Failed to connect to database: %v", err)
	}
	defer func(db *sqlx.DB) {
		err := db.Close()
		if err != nil {
			baseLogger.Sugar().Errorf("Failed to close database connection: %v", err)
		}
	}(db)

	srv := server.NewServer(db, cfg)
	serverCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			baseLogger.Fatal("Server error: ", zap.Error(err))
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
