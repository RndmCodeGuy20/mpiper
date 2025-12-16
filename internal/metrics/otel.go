package metrics

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// InitTracer initializes OpenTelemetry tracing for distributed tracing.
// It sets up a connection to the OTLP collector and configures the trace provider
// with appropriate resource attributes and sampling strategies.
//
// Returns a shutdown function that should be called on application termination
// to ensure all pending traces are exported.
//
// Example usage:
//
//	shutdown := metrics.InitTracer(ctx, logger)
//	defer shutdown(ctx)
func InitTracer(ctx context.Context, logger utils.Logger) func(context.Context) error {
	// Get OTLP collector endpoint from environment with fallback
	endpoint := getEnvOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "otel-collector:4317")

	logger.Sugar().Infof("Initializing OpenTelemetry tracer with endpoint: %s", endpoint)

	// Configure gRPC connection options for production reliability
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
		// TODO: Enable TLS in production environments
		// Replace WithInsecure() with proper TLS configuration:
		// otlptracegrpc.WithTLSCredentials(credentials.NewClientTLSFromCert(certPool, "")),
		otlptracegrpc.WithInsecure(),

		// Retry configuration for handling transient failures
		otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{
			Enabled:         true,
			InitialInterval: 5 * time.Second,
			MaxInterval:     30 * time.Second,
			MaxElapsedTime:  5 * time.Minute,
		}),

		// Connection timeout to prevent hanging on startup
		otlptracegrpc.WithTimeout(10 * time.Second),

		// Compression to reduce network bandwidth
		otlptracegrpc.WithCompressor("gzip"),

		// Additional gRPC dial options
		otlptracegrpc.WithDialOption(
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(100 * 1024 * 1024), // 100MB max message size
			),
		),
		otlptracegrpc.WithDialOption(
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		),
	}

	// Create OTLP trace exporter with production-ready configuration
	exp, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		logger.Sugar().Fatalf("Failed to create OTLP trace exporter: %v", err)
	}

	// Build resource attributes with environment information
	// This helps identify traces by service, version, environment, etc.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			// Service identification
			semconv.ServiceName(getEnvOrDefault("SERVICE_NAME", "mpiper-api")),
			semconv.ServiceVersion(getEnvOrDefault("SERVICE_VERSION", "dev")),

			// Deployment environment
			semconv.DeploymentEnvironment(getEnvOrDefault("DEPLOYMENT_ENV", "development")),

			// Instance identification
			semconv.ServiceInstanceID(getInstanceID()),
		),
		// Automatically detect additional resource attributes
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithOS(),
		resource.WithContainer(),
		resource.WithHost(),
	)
	if err != nil {
		logger.Sugar().Warnf("Failed to create resource attributes: %v", err)
		// Use default resource if creation fails
		res = resource.Default()
	}

	// Configure trace provider with appropriate sampling strategy
	tp := sdktrace.NewTracerProvider(
		// Use batch span processor for better performance
		// Batches spans before sending to reduce network overhead
		sdktrace.WithBatcher(exp,
			sdktrace.WithMaxQueueSize(2048),          // Queue size for batching
			sdktrace.WithBatchTimeout(5*time.Second), // Max time before forcing batch send
			sdktrace.WithMaxExportBatchSize(512),     // Max spans per batch
		),

		// Attach resource attributes to all spans
		sdktrace.WithResource(res),

		// Sampling strategy: always sample in dev, configurable in prod
		// Production: Consider using ParentBased or TraceIDRatioBased sampler
		sdktrace.WithSampler(getSampler()),

		// Span limits to prevent excessive resource usage
		sdktrace.WithSpanLimits(sdktrace.SpanLimits{
			AttributeValueLengthLimit:   4096, // Max attribute value length
			AttributeCountLimit:         128,  // Max attributes per span
			EventCountLimit:             128,  // Max events per span
			LinkCountLimit:              128,  // Max links per span
			AttributePerEventCountLimit: 128,  // Max attributes per event
			AttributePerLinkCountLimit:  128,  // Max attributes per link
		}),
	)

	// Set global tracer provider
	otel.SetTracerProvider(tp)

	// Configure propagators for context propagation across service boundaries
	// W3C Trace Context is the standard, Baggage allows passing custom metadata
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C Trace Context standard
		propagation.Baggage{},      // W3C Baggage for custom metadata
	))

	logger.Sugar().Info("OpenTelemetry tracer initialized successfully")

	// Return shutdown function to be called on application termination
	return tp.Shutdown
}

// getSampler returns the appropriate sampler based on environment configuration
// Development: Always sample for debugging
// Production: Sample based on configuration or use parent-based sampling
func getSampler() sdktrace.Sampler {
	env := os.Getenv("DEPLOYMENT_ENV")

	// Always sample in development for easier debugging
	if env == "development" || env == "dev" || env == "" {
		return sdktrace.AlwaysSample()
	}

	// In production, use parent-based sampling with ratio
	// This samples based on incoming trace decision and ratio for new traces
	samplingRate := 0.1 // 10% sampling rate, adjust based on traffic volume
	if rate := os.Getenv("TRACE_SAMPLING_RATE"); rate != "" {
		if parsed, err := fmt.Sscanf(rate, "%f", &samplingRate); err == nil && parsed == 1 {
			// Successfully parsed custom sampling rate
		}
	}

	return sdktrace.ParentBased(
		sdktrace.TraceIDRatioBased(samplingRate),
	)
}

// getEnvOrDefault retrieves an environment variable or returns a default value
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getInstanceID generates a unique instance identifier
// In Kubernetes, this should be the pod name or hostname
func getInstanceID() string {
	// Try to get hostname (pod name in Kubernetes)
	if hostname, err := os.Hostname(); err == nil {
		return hostname
	}
	// Fallback to a generated ID
	return fmt.Sprintf("instance-%d", time.Now().Unix())
}
