package router

import (
	"math/rand"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/handler"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	appMiddleware "github.com/rndmcodeguy20/mpiper/internal/middleware"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	applogger "github.com/rndmcodeguy20/mpiper/pkg/logger"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
)

const (
	APIVersion        = "v1"
	MiddlewareTimeout = 30 * time.Second
)

// presignRateLimiter removed: per-tenant rate limiting now lives in
// middleware.TenantRateLimitMiddleware (keyed by tenant id, not IP).

func NewRouter(cfg config.EnvConfig, db *sqlx.DB, m *metrics.Metrics) *chi.Mux {
	r := chi.NewRouter()
	cfg2 := config.MustGet()
	logger := applogger.New(applogger.Config{
		ServiceName: cfg2.Otel.ServiceName,
		Environment: cfg2.Environment,
		Level:       applogger.ParseLevel(cfg2.LogLevel),
	})

	allowedOrigins := config.MustGet().CORSAllowedOrigins

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	// Recovery must be the outermost app-level middleware so panics in any
	// inner middleware (logger, cors, tracing, …) are caught and turned into a
	// 500 rather than crashing the process. It takes the base logger directly,
	// so it does not depend on LoggerMiddleware running first.
	r.Use(appMiddleware.RecoveryMiddleware(logger))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(appMiddleware.TracingMiddleware)
	r.Use(appMiddleware.LoggerMiddleware(logger))
	r.Use(middleware.Timeout(MiddlewareTimeout))
	r.Use(appMiddleware.MetricsMiddleware(m))
	r.Use(middleware.Compress(5))
	r.Use(appMiddleware.SlowRequestMiddleware(logger, 2*time.Second))

	assetRepo := repository.NewAssetRepository(db, logger, m)
	outboxRepo := repository.NewOutboxRepository(db, logger)
	assetSvc := service.NewAssetService(assetRepo, outboxRepo, logger, m)
	assetHandler := handler.NewAssetHandler(assetSvc, logger, m)
	apiKeyRepo := repository.NewAPIKeyRepository(db, logger)
	idempotencyRepo := repository.NewIdempotencyRepository(db, logger)
	idempotencyTTL := config.MustGet().IdempotencyTTL

	// Routes
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok", "message": "Welcome to mpiper API"}`))
	})

	r.Get("/metric_test", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(rand.Intn(500)) * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok", "message": "Metric test endpoint"}`))
	})

	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status": "ok", "message": "mpiper is healthy"}`))
	})

	r.Route("/api/"+APIVersion, func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status": "ok", "message": "mpiper API v1"}`))
		})
		r.Get("/status", func(w http.ResponseWriter, r *http.Request) {
			utils.RespondJSON(w, map[string]string{"status": "ok", "message": "mpiper API is running"}, http.StatusOK)
		})

		r.Route("/storage", func(r chi.Router) {
			quotaCfg := config.MustGet().Quota
			r.Use(appMiddleware.AuthMiddleware(logger, apiKeyRepo))
			r.Use(appMiddleware.IdempotencyMiddleware(logger, idempotencyRepo, idempotencyTTL))
			r.Use(appMiddleware.TenantRateLimitMiddleware(logger, m, quotaCfg.RateLimitRPS, quotaCfg.RateLimitBurst))
			r.Use(appMiddleware.TenantQuotaMiddleware(logger, m, assetRepo, quotaCfg.AssetQuota))
			r.Post("/presign", assetHandler.CreateAsset)
		})

		r.Route("/assets", func(r chi.Router) {
			r.Use(appMiddleware.AuthMiddleware(logger, apiKeyRepo))
			r.Use(appMiddleware.IdempotencyMiddleware(logger, idempotencyRepo, idempotencyTTL))
			r.Get("/{assetID}/complete", assetHandler.MarkAssetUploaded)
		})

		r.Route("/webhooks", func(r chi.Router) {
			r.Use(appMiddleware.AuthMiddleware(logger, apiKeyRepo))
			webhookRepo := repository.NewWebhookRepository(db, logger)
			webhookSvc := service.NewWebhookService(webhookRepo, logger)
			webhookHandler := handler.NewWebhookHandler(webhookSvc, logger)
			r.Post("/", webhookHandler.Create)
			r.Get("/", webhookHandler.List)
			r.Delete("/{id}", webhookHandler.Delete)
		})
	})

	return r
}
