package metrics

import (
	"go.uber.org/zap"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// NewTestMetrics builds a *Metrics backed by a ManualReader instead of the OTLP
// exporter, so tests can record against the real instruments and then read them
// back via the returned reader (reader.Collect). It runs the same init* funcs as
// InitMetrics, so every instrument is non-nil and behaves identically.
//
// This lives in a non-_test.go file so it is importable from other packages'
// tests (e.g. internal/webhook). It pulls in no dependencies beyond sdkmetric,
// which is already a production dependency of this package.
func NewTestMetrics() (*Metrics, *sdkmetric.ManualReader) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("mpiper-test")

	logger := zap.NewNop()
	m := &Metrics{meter: meter}
	initHTTPMetrics(m, meter, logger)
	initBusinessMetrics(m, meter, logger)
	initStorageMetrics(m, meter, logger)
	initDatabaseMetrics(m, meter, logger)
	initQueueMetrics(m, meter, logger)
	initOutboxMetrics(m, meter, logger)
	initWebhookMetrics(m, meter, logger)
	initSystemMetrics(m, meter, logger)
	return m, reader
}
