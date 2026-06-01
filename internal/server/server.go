package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/router"
	applogger "github.com/rndmcodeguy20/mpiper/pkg/logger"
	"go.uber.org/zap"
)

type AppServer struct {
	db         *sqlx.DB
	logger     *zap.Logger
	httpServer *http.Server
}

func NewServer(db *sqlx.DB, cfg config.EnvConfig, m *metrics.Metrics) *AppServer {
	cfg2 := config.MustGet()
	l := applogger.New(applogger.Config{
		ServiceName: cfg2.Otel.ServiceName,
		Environment: cfg2.Environment,
		Level:       applogger.ParseLevel(cfg2.LogLevel),
	})

	srv := &http.Server{
		Addr:    cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port),
		Handler: router.NewRouter(cfg, db, m),
	}

	return &AppServer{
		db:         db,
		logger:     l,
		httpServer: srv,
	}
}

func (s *AppServer) Start() error {
	s.logger.Sugar().Infof("Starting server on %s", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *AppServer) Stop(ctx context.Context) error {
	s.logger.Info("Stopping server")
	return s.httpServer.Shutdown(ctx)
}
