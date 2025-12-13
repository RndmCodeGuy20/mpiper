package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/rndmcodeguy20/mpiper/pkg/utils"
)

// contextKey is a custom type for context keys
type contextKey string

const loggerKey contextKey = "logger"

// LoggerFromContext retrieves the logger from context
func LoggerFromContext(ctx context.Context) *utils.Logger {
	if logger, ok := ctx.Value(loggerKey).(*utils.Logger); ok {
		return logger
	}
	// Return default logger if not found
	return utils.NewLogger()
}

// LoggerMiddleware integrates with Chi's RequestID and other middleware
func LoggerMiddleware(logger *utils.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Get request ID from Chi middleware (if available)
			requestID := middleware.GetReqID(r.Context())
			if requestID == "" {
				requestID = generateRequestID()
			}

			// Create context logger with all Chi-provided fields
			ctxLogger := logger.WithFields(map[string]interface{}{
				"request_id":  requestID,
				"method":      r.Method,
				"path":        r.URL.Path,
				"remote_addr": r.RemoteAddr,
				"user_agent":  r.UserAgent(),
				"proto":       r.Proto,
			})

			// Store logger in context for downstream handlers
			ctx := context.WithValue(r.Context(), loggerKey, ctxLogger)
			r = r.WithContext(ctx)

			// Wrap response writer to capture status and size
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			// Add request ID to response headers
			ww.Header().Set("X-Request-ID", requestID)

			// Log incoming request
			ctxLogger.Info("Incoming request")

			// Call next handler
			next.ServeHTTP(ww, r)

			// Calculate duration
			duration := time.Since(start)

			// Determine log level based on status code
			status := ww.Status()
			logFunc := ctxLogger.Info
			if status >= 500 {
				logFunc = ctxLogger.Error
			} else if status >= 400 {
				logFunc = ctxLogger.Warn
			}

			// Log completed request
			logFunc("Request completed",
				zap.Int("status", status),
				zap.String("duration", durationInUnits(duration)),
				zap.Int("bytes_written", ww.BytesWritten()),
			)
		})
	}
}

func durationInUnits(d time.Duration) string {
	if d >= time.Second {
		return d.Truncate(time.Millisecond).String()
	} else if d >= time.Millisecond {
		return d.Truncate(time.Microsecond).String()
	}
	return d.String()
}

func generateRequestID() string {
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}
