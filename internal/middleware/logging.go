package middleware

import (
	"context"
	"math/rand/v2"
	"net/http"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	applogger "github.com/rndmcodeguy20/mpiper/pkg/logger"
	"go.uber.org/zap"
)

// LoggerFromContext retrieves the request-scoped logger from ctx.
func LoggerFromContext(ctx context.Context) *zap.Logger {
	return applogger.FromContext(ctx)
}

// LoggerMiddleware injects a request-scoped logger into the context and logs
// request/response details.
func LoggerMiddleware(l *zap.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			requestID := chiMiddleware.GetReqID(r.Context())
			if requestID == "" {
				requestID = generateRequestID()
			}

			reqLogger := l.With(
				zap.String("request_id", requestID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("user_agent", r.UserAgent()),
				zap.String("proto", r.Proto),
			)

			// Stamp trace_id/span_id from the active span (TracingMiddleware runs
			// before this) so request logs cross-link to their Tempo trace and
			// any handler/service using the context logger inherits the IDs.
			reqLogger = applogger.WithTrace(r.Context(), reqLogger)

			ctx := applogger.WithLogger(r.Context(), reqLogger)
			r = r.WithContext(ctx)

			ww := chiMiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			ww.Header().Set("X-Request-ID", requestID)

			reqLogger.Info("Incoming request")

			next.ServeHTTP(ww, r)

			status := ww.Status()
			duration := time.Since(start)

			logFn := reqLogger.Info
			if status >= 500 {
				logFn = reqLogger.Error
			} else if status >= 400 {
				logFn = reqLogger.Warn
			}

			logFn("Request completed",
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
		// math/rand/v2 is concurrency-safe and unbiased; a log-correlation ID
		// needs neither crypto strength nor per-call seeding.
		b[i] = charset[rand.IntN(len(charset))]
	}
	return string(b)
}
