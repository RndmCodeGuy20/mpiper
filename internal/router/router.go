package router

import (
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/handler"
	appMiddleware "github.com/rndmcodeguy20/mpiper/internal/middleware"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/internal/service"
	applogger "github.com/rndmcodeguy20/mpiper/pkg/logger"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"github.com/rndmcodeguy20/mpiper/pkg/utils/storagex"
	"golang.org/x/time/rate"
)

const (
	APIVersion        = "v1"
	MiddlewareTimeout = 30 * time.Second
)

// presignRateLimiter returns a per-IP rate-limit middleware.
// Each IP is allowed 10 requests/s with a burst of 20.
func presignRateLimiter() func(http.Handler) http.Handler {
	type entry struct {
		lim      *rate.Limiter
		lastSeen time.Time
	}
	var (
		mu      sync.Mutex
		clients = make(map[string]*entry)
	)
	// Evict IPs not seen in the last 5 minutes to prevent unbounded growth.
	go func() {
		for range time.Tick(time.Minute) {
			mu.Lock()
			for ip, e := range clients {
				if time.Since(e.lastSeen) > 5*time.Minute {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()

	getLimiter := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		e, ok := clients[ip]
		if !ok {
			e = &entry{lim: rate.NewLimiter(rate.Limit(10), 20)}
			clients[ip] = e
		}
		e.lastSeen = time.Now()
		return e.lim
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				ip = strings.SplitN(xff, ",", 2)[0]
			}
			if !getLimiter(strings.TrimSpace(ip)).Allow() {
				http.Error(w, `{"status":"error","message":"rate limit exceeded"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

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
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(appMiddleware.LoggerMiddleware(logger))
	r.Use(middleware.Timeout(MiddlewareTimeout))
	r.Use(appMiddleware.TracingMiddleware)
	r.Use(appMiddleware.MetricsMiddleware(m))
	r.Use(appMiddleware.RecoveryMiddleware(logger))
	r.Use(middleware.Compress(5))
	r.Use(appMiddleware.SlowRequestMiddleware(logger, 2*time.Second))

	assetRepo := repository.NewAssetRepository(db, logger, m)
	assetSvc := service.NewAssetService(&cfg.Redis, storagex.GCPProvider, assetRepo, logger, m)
	assetHandler := handler.NewAssetHandler(assetSvc, logger, m)

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
			r.Use(appMiddleware.AuthMiddleware(logger))
			r.With(presignRateLimiter()).Post("/presign", assetHandler.CreateAsset)
		})

		r.Route("/assets", func(r chi.Router) {
			r.Use(appMiddleware.AuthMiddleware(logger))
			r.Get("/{assetID}/complete", assetHandler.MarkAssetUploaded)
		})
	})

	return r
}
