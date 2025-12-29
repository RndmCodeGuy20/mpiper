package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type metricsResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
}

func (w *metricsResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *metricsResponseWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	return n, err
}

// MetricsMiddleware records HTTP metrics for incoming requests.
// Safe for Prometheus cardinality and production traffic.
func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unknown"
		}

		wrapped := &metricsResponseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.method", r.Method),
			attribute.String("http.route", route),
		}

		// Active requests
		if metrics.HTTPActiveRequests != nil {
			metrics.HTTPActiveRequests.Add(
				r.Context(),
				1,
				metric.WithAttributes(attrs...),
			)
			defer metrics.HTTPActiveRequests.Add(
				r.Context(),
				-1,
				metric.WithAttributes(attrs...),
			)
		}

		// Panic safety: still emit metrics
		defer func() {
			if rec := recover(); rec != nil {
				wrapped.statusCode = http.StatusInternalServerError
				recordHTTPMetrics(r, wrapped, start, attrs)
				panic(rec)
			}
		}()

		next.ServeHTTP(wrapped, r)

		recordHTTPMetrics(r, wrapped, start, attrs)
	})
}

func recordHTTPMetrics(
	r *http.Request,
	w *metricsResponseWriter,
	start time.Time,
	baseAttrs []attribute.KeyValue,
) {
	duration := time.Since(start).Seconds()

	attrs := append(
		baseAttrs,
		attribute.Int("http.status_code", w.statusCode),
	)

	if metrics.HTTPRequestDuration != nil {
		metrics.HTTPRequestDuration.Record(
			r.Context(),
			duration,
			metric.WithAttributes(attrs...),
		)
	}

	if metrics.HTTPRequestCount != nil {
		metrics.HTTPRequestCount.Add(
			r.Context(),
			1,
			metric.WithAttributes(attrs...),
		)
	}

	if metrics.HTTPRequestSize != nil {
		reqSize := r.ContentLength
		if reqSize < 0 {
			reqSize = 0
		}
		metrics.HTTPRequestSize.Record(
			r.Context(),
			reqSize,
			metric.WithAttributes(attrs...),
		)
	}

	if metrics.HTTPResponseSize != nil {
		metrics.HTTPResponseSize.Record(
			r.Context(),
			w.bytesWritten,
			metric.WithAttributes(attrs...),
		)
	}
}
