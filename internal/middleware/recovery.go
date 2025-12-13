package middleware

import (
	"net/http"

	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

// RecoveryMiddleware catches panics and logs them
func RecoveryMiddleware(logger *utils.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// Get context logger if available
					ctxLogger := LoggerFromContext(r.Context())

					ctxLogger.Error("Panic recovered",
						zap.Any("panic", err),
						zap.String("method", r.Method),
						zap.String("path", r.URL.Path),
						zap.Stack("stack"),
					)

					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(`{"error": "Internal Server Error"}`))
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
