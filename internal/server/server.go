package server

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/router"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
)

type AppServer struct {
	db         *sqlx.DB
	logger     *utils.Logger
	httpServer *http.Server
}

func NewServer(db *sqlx.DB, cfg config.EnvConfig) *AppServer {
	r := router.NewRouter(cfg, db)
	logger := utils.NewLogger()

	srv := &http.Server{
		Addr:    cfg.Server.Host + ":" + strconv.Itoa(cfg.Server.Port),
		Handler: r,
	}

	return &AppServer{
		db:         db,
		logger:     logger,
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
