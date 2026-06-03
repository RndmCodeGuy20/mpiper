package metrics

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/config"
	"go.uber.org/zap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// InitTracer initializes OpenTelemetry tracing for distributed tracing.
// Returns a shutdown function that should be called on application termination.
func InitTracer(ctx context.Context, logger *zap.Logger) func(context.Context) error {
	otelCfg := config.MustGet().Otel
	endpoint := stripURLScheme(otelCfg.Endpoint)

	logger.Sugar().Infof("Initializing OpenTelemetry tracer with endpoint: %s", endpoint)

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{
			Enabled:         true,
			InitialInterval: 5 * time.Second,
			MaxInterval:     30 * time.Second,
			MaxElapsedTime:  5 * time.Minute,
		}),
		otlptracegrpc.WithTimeout(10 * time.Second),
		otlptracegrpc.WithCompressor("gzip"),
		otlptracegrpc.WithDialOption(
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(100 * 1024 * 1024)),
		),
	}
	if otelCfg.TLSInsecure {
		opts = append(opts,
			otlptracegrpc.WithInsecure(),
			otlptracegrpc.WithDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		)
	} else {
		opts = append(opts,
			otlptracegrpc.WithDialOption(grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(nil, ""))),
		)
	}

	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create OTLP trace exporter: %v", err)
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

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(getSampler(otelCfg)),
		sdktrace.WithSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit:   4096,
			AttributeCountLimit:         128,
			EventCountLimit:             128,
			LinkCountLimit:              128,
			AttributePerEventCountLimit: 128,
			AttributePerLinkCountLimit:  128,
		}),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logger.Sugar().Info("OpenTelemetry tracer initialized successfully")
	return tp.Shutdown
}

func getSampler(otelCfg config.OtelConfig) sdktrace.Sampler {
	env := otelCfg.DeploymentEnv
	if env == "development" || env == "dev" || env == "" {
		return sdktrace.AlwaysSample()
	}
	return sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(otelCfg.TraceSamplingRate),
	)
}

// getInstanceID returns the hostname (pod name in Kubernetes) or a fallback.
func getInstanceID() string {
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	return fmt.Sprintf("instance-%d", time.Now().Unix())
}

// stripURLScheme removes grpc://, http://, https:// from the endpoint.
func stripURLScheme(endpoint string) string {
	for _, scheme := range []string{"grpc://", "http://", "https://"} {
		if len(endpoint) > len(scheme) && endpoint[:len(scheme)] == scheme {
			return endpoint[len(scheme):]
		}
	}
	return endpoint
}
