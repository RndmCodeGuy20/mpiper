package router

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/handler"
	appMiddleware "github.com/rndmcodeguy20/mpiper/internal/middleware"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"github.com/rndmcodeguy20/mpiper/pkg/utils/storagex"
)

const (
	APIVersion        = "v1"
	MiddlewareTimeout = 30 * time.Second
)

func NewRouter(cfg config.EnvConfig, db *sqlx.DB) *chi.Mux {
	r := chi.NewRouter()
	logger := utils.NewLogger()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(appMiddleware.LoggerMiddleware(logger))
	r.Use(appMiddleware.RecoveryMiddleware(logger))
	r.Use(middleware.Timeout(MiddlewareTimeout))

	r.Use(middleware.Compress(5))
	r.Use(appMiddleware.SlowRequestMiddleware(logger, 2*time.Second))

	//domainRepository := repository.NewDomainRepository(db, logger)
	//domainService := service.NewDomainService(domainRepository, cfg, logger)
	//domainHandler := handler.NewDomainHandler(domainService, logger)
	//
	//userRepository := repository.NewUserRepository(db, logger)
	//authService := service.NewAuthService(logger, userRepository)
	//userService := service.NewUserService(logger, userRepository)
	//userHandler := handler.NewUserHandler(userService, authService, logger)

	assetRepo := repository.NewAssetRepository(db, logger)
	assetSvc := service.NewAssetService(&cfg.Redis, storagex.GCPProvider, assetRepo, logger)
	assetHandler := handler.NewAssetHandler(assetSvc, logger)

	// API Routes
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{"status": "ok", "message": "Welcome to mpiper API"}`))
		if err != nil {
			return
		}
	})

	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	r.Route("/api/"+APIVersion, func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status": "ok", "message": "mpiper API v1"}`))
			if err != nil {
				return
			}
		})
		r.Get("/status", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			utils.RespondJSON(
				w,
				map[string]string{"status": "ok", "message": "mpiper API is running"},
				http.StatusOK,
			)
		})

		r.Route("/storage", func(r chi.Router) {
			r.Post("/presign", assetHandler.CreateAsset)
		})

		r.Route("/assets", func(r chi.Router) {
			r.Get("/{assetID}/complete", assetHandler.MarkAssetUploaded)
		})
	})

	return r
}
