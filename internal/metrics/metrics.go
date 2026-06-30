package metrics

import (
	"context"
	"runtime"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/config"
	"go.uber.org/zap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Metrics holds all OTel instruments. Pass *Metrics via DI; never use package-level globals.
type Metrics struct {
	meter metric.Meter

	HTTPRequestDuration metric.Float64Histogram
	HTTPRequestCount    metric.Int64Counter
	HTTPRequestSize     metric.Int64Histogram
	HTTPResponseSize    metric.Int64Histogram
	HTTPActiveRequests  metric.Int64UpDownCounter

	AssetUploadTotal        metric.Int64Counter
	AssetUploadDuration     metric.Float64Histogram
	AssetProcessingTotal    metric.Int64Counter
	AssetProcessingSuccess  metric.Int64Counter
	AssetProcessingFailed   metric.Int64Counter
	AssetProcessingDuration metric.Float64Histogram
	AssetSizeBytes          metric.Int64Histogram

	StorageOperationDuration metric.Float64Histogram
	StorageOperationTotal    metric.Int64Counter
	StorageOperationErrors   metric.Int64Counter

	DBQueryDuration     metric.Float64Histogram
	DBQueryTotal        metric.Int64Counter
	DBQueryErrors       metric.Int64Counter
	DBConnectionsActive metric.Int64UpDownCounter
	DBConnectionsIdle   metric.Int64UpDownCounter
	DBTransactionTotal  metric.Int64Counter
	DBTransactionErrors metric.Int64Counter

	QueueMessagePublished metric.Int64Counter
	QueueMessageConsumed  metric.Int64Counter
	QueueMessageFailed    metric.Int64Counter
	QueueDepth            metric.Int64ObservableGauge
	QueueProcessingLag    metric.Float64Histogram

	OutboxPublishedTotal  metric.Int64Counter
	OutboxPublishFailures metric.Int64Counter
	OutboxRelayLagSeconds metric.Float64Histogram
	OutboxPendingGauge    metric.Int64ObservableGauge

	WebhookDeliveryTotal    metric.Int64Counter
	WebhookDeliveryDuration metric.Float64Histogram
	WebhookDeliveryFailures metric.Int64Counter
	WebhookPendingGauge     metric.Int64ObservableGauge

	SystemMemoryUsage     metric.Int64ObservableGauge
	SystemGoroutineCount  metric.Int64ObservableGauge
	SystemGCPauseDuration metric.Float64Histogram
}

// RegisterQueueDepthFunc wires an XLEN-style callback to the QueueDepth gauge.
// Call after the queue client is initialised.
func (m *Metrics) RegisterQueueDepthFunc(fn func(context.Context) (int64, error)) error {
	_, err := m.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := fn(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(m.QueueDepth, n)
		return nil
	}, m.QueueDepth)
	return err
}

// RegisterOutboxPendingFunc wires a callback to the OutboxPendingGauge.
func (m *Metrics) RegisterOutboxPendingFunc(fn func(context.Context) (int64, error)) error {
	_, err := m.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := fn(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(m.OutboxPendingGauge, n)
		return nil
	}, m.OutboxPendingGauge)
	return err
}

func InitMetrics(ctx context.Context, logger *zap.Logger) (*Metrics, func(context.Context) error) {
	otelCfg := config.MustGet().Otel
	endpoint := stripURLScheme(otelCfg.Endpoint)

	logger.Sugar().Infof("Initializing OpenTelemetry metrics with endpoint: %s", endpoint)

	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithTimeout(10 * time.Second),
		otlpmetricgrpc.WithCompressor("gzip"),
		otlpmetricgrpc.WithDialOption(
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(100 * 1024 * 1024)),
		),
	}
	if otelCfg.TLSInsecure {
		opts = append(opts,
			otlpmetricgrpc.WithInsecure(),
			otlpmetricgrpc.WithDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		)
	} else {
		opts = append(opts,
			otlpmetricgrpc.WithDialOption(grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, ""))),
		)
	}

	exp, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create OTLP metric exporter: %v", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(otelCfg.ServiceName),
			semconv.ServiceVersion(otelCfg.ServiceVersion),
			semconv.DeploymentEnvironment(otelCfg.DeploymentEnv),
			semconv.ServiceInstanceID(getInstanceID()),
		),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithContainer(),
		resource.WithHost(),
	)
	if err != nil {
		logger.Sugar().Warnf("Failed to create resource attributes: %v", err)
		res = resource.Default()
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
		sdkmetric.WithView(
			sdkmetric.NewView(
				sdkmetric.Instrument{Name: "http.server.request.duration", Kind: sdkmetric.InstrumentKindHistogram},
				sdkmetric.Stream{
					Aggregation: sdkmetric.AggregationExplicitBucketHistogram{
						Boundaries: []float64{0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10},
					},
				},
			),
		),
	)

	otel.SetMeterProvider(mp)
	meter := mp.Meter("mpiper-api")

	m := &Metrics{meter: meter}
	initHTTPMetrics(m, meter, logger)
	initBusinessMetrics(m, meter, logger)
	initStorageMetrics(m, meter, logger)
	initDatabaseMetrics(m, meter, logger)
	initQueueMetrics(m, meter, logger)
	initOutboxMetrics(m, meter, logger)
	initWebhookMetrics(m, meter, logger)
	initSystemMetrics(m, meter, logger)

	logger.Sugar().Info("OpenTelemetry metrics initialized successfully")
	return m, mp.Shutdown
}

func initHTTPMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.HTTPRequestDuration, err = meter.Float64Histogram("http.server.request.duration",
		metric.WithDescription("Duration of HTTP requests in seconds"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create HTTP request duration: %v", err)
	}
	m.HTTPRequestCount, err = meter.Int64Counter("http.server.request.count",
		metric.WithDescription("Total number of HTTP requests"), metric.WithUnit("{request}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create HTTP request counter: %v", err)
	}
	m.HTTPRequestSize, err = meter.Int64Histogram("http.server.request.size",
		metric.WithDescription("Size of HTTP request in bytes"), metric.WithUnit("By"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create HTTP request size: %v", err)
	}
	m.HTTPResponseSize, err = meter.Int64Histogram("http.server.response.size",
		metric.WithDescription("Size of HTTP response in bytes"), metric.WithUnit("By"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create HTTP response size: %v", err)
	}
	m.HTTPActiveRequests, err = meter.Int64UpDownCounter("http.server.active_requests",
		metric.WithDescription("Number of active HTTP requests"), metric.WithUnit("{request}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create active requests counter: %v", err)
	}
}

func initBusinessMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.AssetUploadTotal, err = meter.Int64Counter("asset.upload.total",
		metric.WithDescription("Total number of asset uploads"), metric.WithUnit("{upload}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset upload counter: %v", err)
	}
	m.AssetUploadDuration, err = meter.Float64Histogram("asset.upload.duration",
		metric.WithDescription("Duration of asset upload operations"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset upload duration: %v", err)
	}
	m.AssetProcessingTotal, err = meter.Int64Counter("asset.processing.total",
		metric.WithDescription("Total number of assets processed"), metric.WithUnit("{asset}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset processing counter: %v", err)
	}
	m.AssetProcessingSuccess, err = meter.Int64Counter("asset.processing.success",
		metric.WithDescription("Number of successfully processed assets"), metric.WithUnit("{asset}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset processing success counter: %v", err)
	}
	m.AssetProcessingFailed, err = meter.Int64Counter("asset.processing.failed",
		metric.WithDescription("Number of failed asset processing attempts"), metric.WithUnit("{asset}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset processing failed counter: %v", err)
	}
	m.AssetProcessingDuration, err = meter.Float64Histogram("asset.processing.duration",
		metric.WithDescription("Duration of asset processing"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset processing duration: %v", err)
	}
	m.AssetSizeBytes, err = meter.Int64Histogram("asset.size",
		metric.WithDescription("Size of assets in bytes"), metric.WithUnit("By"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create asset size histogram: %v", err)
	}
}

func initStorageMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.StorageOperationDuration, err = meter.Float64Histogram("storage.operation.duration",
		metric.WithDescription("Duration of storage operations"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create storage operation duration: %v", err)
	}
	m.StorageOperationTotal, err = meter.Int64Counter("storage.operation.total",
		metric.WithDescription("Total number of storage operations"), metric.WithUnit("{operation}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create storage operation counter: %v", err)
	}
	m.StorageOperationErrors, err = meter.Int64Counter("storage.operation.errors",
		metric.WithDescription("Number of storage operation errors"), metric.WithUnit("{error}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create storage operation errors: %v", err)
	}
}

func initDatabaseMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.DBQueryDuration, err = meter.Float64Histogram("db.query.duration",
		metric.WithDescription("Duration of database queries"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB query duration: %v", err)
	}
	m.DBQueryTotal, err = meter.Int64Counter("db.query.total",
		metric.WithDescription("Total number of database queries"), metric.WithUnit("{query}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB query counter: %v", err)
	}
	m.DBQueryErrors, err = meter.Int64Counter("db.query.errors",
		metric.WithDescription("Number of database query errors"), metric.WithUnit("{error}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB query errors: %v", err)
	}
	m.DBConnectionsActive, err = meter.Int64UpDownCounter("db.connections.active",
		metric.WithDescription("Number of active database connections"), metric.WithUnit("{connection}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB active connections: %v", err)
	}
	m.DBConnectionsIdle, err = meter.Int64UpDownCounter("db.connections.idle",
		metric.WithDescription("Number of idle database connections"), metric.WithUnit("{connection}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB idle connections: %v", err)
	}
	m.DBTransactionTotal, err = meter.Int64Counter("db.transaction.total",
		metric.WithDescription("Total number of database transactions"), metric.WithUnit("{transaction}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB transaction counter: %v", err)
	}
	m.DBTransactionErrors, err = meter.Int64Counter("db.transaction.errors",
		metric.WithDescription("Number of database transaction errors"), metric.WithUnit("{error}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create DB transaction errors: %v", err)
	}
}

func initQueueMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.QueueMessagePublished, err = meter.Int64Counter("queue.message.published",
		metric.WithDescription("Number of messages published to queue"), metric.WithUnit("{message}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create queue published counter: %v", err)
	}
	m.QueueMessageConsumed, err = meter.Int64Counter("queue.message.consumed",
		metric.WithDescription("Number of messages consumed from queue"), metric.WithUnit("{message}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create queue consumed counter: %v", err)
	}
	m.QueueMessageFailed, err = meter.Int64Counter("queue.message.failed",
		metric.WithDescription("Number of failed queue messages"), metric.WithUnit("{message}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create queue failed counter: %v", err)
	}
	// Callback registered later via RegisterQueueDepthFunc once the Redis client is available.
	m.QueueDepth, err = meter.Int64ObservableGauge("queue.depth",
		metric.WithDescription("Current number of messages in the queue"), metric.WithUnit("{message}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create queue depth gauge: %v", err)
	}
	m.QueueProcessingLag, err = meter.Float64Histogram("queue.processing.lag",
		metric.WithDescription("Queue message processing lag in seconds"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create queue processing lag: %v", err)
	}
}

func initOutboxMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.OutboxPublishedTotal, err = meter.Int64Counter("outbox.published.total",
		metric.WithDescription("Total outbox events published to stream"), metric.WithUnit("{event}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create outbox published counter: %v", err)
	}
	m.OutboxPublishFailures, err = meter.Int64Counter("outbox.publish.failures",
		metric.WithDescription("Total outbox publish failures"), metric.WithUnit("{event}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create outbox publish failures counter: %v", err)
	}
	m.OutboxRelayLagSeconds, err = meter.Float64Histogram("outbox.relay.lag",
		metric.WithDescription("Age of oldest pending outbox row in seconds"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create outbox relay lag histogram: %v", err)
	}
	m.OutboxPendingGauge, err = meter.Int64ObservableGauge("outbox.pending",
		metric.WithDescription("Number of pending outbox events"), metric.WithUnit("{event}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create outbox pending gauge: %v", err)
	}
}

func initSystemMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	var memStats runtime.MemStats

	m.SystemMemoryUsage, err = meter.Int64ObservableGauge("system.memory.usage",
		metric.WithDescription("System memory usage in bytes"),
		metric.WithUnit("By"),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			runtime.ReadMemStats(&memStats)
			observer.Observe(int64(memStats.Alloc))
			return nil
		}),
	)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create memory usage gauge: %v", err)
	}
	m.SystemGoroutineCount, err = meter.Int64ObservableGauge("system.goroutine.count",
		metric.WithDescription("Number of goroutines"),
		metric.WithUnit("{goroutine}"),
		metric.WithInt64Callback(func(_ context.Context, observer metric.Int64Observer) error {
			observer.Observe(int64(runtime.NumGoroutine()))
			return nil
		}),
	)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create goroutine count gauge: %v", err)
	}
	m.SystemGCPauseDuration, err = meter.Float64Histogram("system.gc.pause.duration",
		metric.WithDescription("GC pause duration in seconds"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create GC pause duration: %v", err)
	}
}

func initWebhookMetrics(m *Metrics, meter metric.Meter, logger *zap.Logger) {
	var err error
	m.WebhookDeliveryTotal, err = meter.Int64Counter("webhook.delivery.total",
		metric.WithDescription("Total webhook deliveries attempted"), metric.WithUnit("{delivery}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create webhook delivery counter: %v", err)
	}
	m.WebhookDeliveryDuration, err = meter.Float64Histogram("webhook.delivery.duration",
		metric.WithDescription("Duration of webhook delivery HTTP calls"), metric.WithUnit("s"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create webhook delivery duration: %v", err)
	}
	m.WebhookDeliveryFailures, err = meter.Int64Counter("webhook.delivery.failures",
		metric.WithDescription("Total webhook delivery failures"), metric.WithUnit("{delivery}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create webhook delivery failures counter: %v", err)
	}
	m.WebhookPendingGauge, err = meter.Int64ObservableGauge("webhook.pending",
		metric.WithDescription("Number of pending webhook deliveries"), metric.WithUnit("{delivery}"))
	if err != nil {
		logger.Sugar().Fatalf("Failed to create webhook pending gauge: %v", err)
	}
}

// RegisterWebhookPendingFunc wires a callback to the WebhookPendingGauge.
func (m *Metrics) RegisterWebhookPendingFunc(fn func(context.Context) (int64, error)) error {
	_, err := m.meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		n, err := fn(ctx)
		if err != nil {
			return err
		}
		o.ObserveInt64(m.WebhookPendingGauge, n)
		return nil
	}, m.WebhookPendingGauge)
	return err
}
