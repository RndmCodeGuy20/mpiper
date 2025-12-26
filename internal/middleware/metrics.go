package middleware

import (
	"net/http"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricsMiddleware records metrics for incoming HTTP requests
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a custom response writer to capture status code
		wrapped := &metricsResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Call the next handler
		next.ServeHTTP(wrapped, r)

		// Calculate request duration in seconds
		duration := time.Since(start).Seconds()

		// Record the duration with labels
		attrs := []attribute.KeyValue{
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
			attribute.Int("http.status_code", wrapped.statusCode),
		}

		// Record histogram metric
		if metrics.HTTPRequestDuration != nil {
			metrics.HTTPRequestDuration.Record(r.Context(), duration, metric.WithAttributes(attrs...))
		}

		// Increment counter
		if metrics.HTTPRequestCount != nil {
			metrics.HTTPRequestCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))
		}
	})
}

// metricsResponseWriter wraps http.ResponseWriter to capture status code
type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (mrw *metricsResponseWriter) WriteHeader(code int) {
	mrw.statusCode = code
	mrw.ResponseWriter.WriteHeader(code)
}
