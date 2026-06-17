package middleware

import (
	"net/http"
	"time"

	"go.uber.org/zap"
)

// SlowRequestMiddleware logs requests that exceed threshold.
func SlowRequestMiddleware(l *zap.Logger, threshold time.Duration) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			next.ServeHTTP(w, r)

			if duration := time.Since(start); duration > threshold {
				ctxLogger := LoggerFromContext(r.Context())
				ctxLogger.Warn("Slow request detected",
					zap.Duration("duration", duration),
					zap.Duration("threshold", threshold),
				)
			}
		})
	}
}
