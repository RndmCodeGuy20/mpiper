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

func MetricsMiddleware(m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			wrapped := &metricsResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			// In-flight gauge: keyed by method only. The route pattern is not yet
			// known here (chi populates it during routing, after this middleware),
			// so it would be "unknown"; using it on +1/-1 still nets to zero but
			// adds no value. Per-route labels are applied post-routing below.
			inflightAttrs := []attribute.KeyValue{attribute.String("http.method", r.Method)}

			if m != nil {
				m.HTTPActiveRequests.Add(r.Context(), 1, metric.WithAttributes(inflightAttrs...))
				defer m.HTTPActiveRequests.Add(r.Context(), -1, metric.WithAttributes(inflightAttrs...))
			}

			defer func() {
				if rec := recover(); rec != nil {
					wrapped.statusCode = http.StatusInternalServerError
					recordHTTPMetrics(m, r, wrapped, start)
					panic(rec)
				}
			}()

			next.ServeHTTP(wrapped, r)
			recordHTTPMetrics(m, r, wrapped, start)
		})
	}
}

func recordHTTPMetrics(m *metrics.Metrics, r *http.Request, w *metricsResponseWriter, start time.Time) {
	if m == nil {
		return
	}
	// chi populates the matched route pattern during routing, so it is only
	// available now (after ServeHTTP). Reading it earlier yields "" — the source
	// of the previous "unknown" http_route label that broke per-route SLOs.
	route := chi.RouteContext(r.Context()).RoutePattern()
	if route == "" {
		route = "unknown"
	}

	duration := time.Since(start).Seconds()
	attrs := []attribute.KeyValue{
		attribute.String("http.method", r.Method),
		attribute.String("http.route", route),
		attribute.Int("http.status_code", w.statusCode),
	}

	m.HTTPRequestDuration.Record(r.Context(), duration, metric.WithAttributes(attrs...))
	m.HTTPRequestCount.Add(r.Context(), 1, metric.WithAttributes(attrs...))

	reqSize := r.ContentLength
	if reqSize < 0 {
		reqSize = 0
	}
	m.HTTPRequestSize.Record(r.Context(), reqSize, metric.WithAttributes(attrs...))
	m.HTTPResponseSize.Record(r.Context(), w.bytesWritten, metric.WithAttributes(attrs...))
}
