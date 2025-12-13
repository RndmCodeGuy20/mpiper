package middleware

import (
	"net/http"
	"time"

	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

// SlowRequestMiddleware logs slow requests
func SlowRequestMiddleware(logger *utils.Logger, threshold time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			next.ServeHTTP(w, r)

			duration := time.Since(start)
			if duration > threshold {
				ctxLogger := LoggerFromContext(r.Context())
				ctxLogger.Warn("Slow request detected",
					zap.Duration("duration", duration),
					zap.Duration("threshold", threshold),
				)
			}
		})
	}
}
