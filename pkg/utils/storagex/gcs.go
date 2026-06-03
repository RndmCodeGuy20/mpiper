package storagex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

type gcsStorage struct {
	client         *storage.Client
	secretAccessID string
	privateKey     []byte
	logger         *zap.Logger
	provider       string
	m              *metrics.Metrics
}

func NewGCSStorage(ctx context.Context, projectID string, m *metrics.Metrics) (StorageX, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &gcsStorage{client: client, m: m}, nil
}

func NewGCSStorageFromServiceAccountJSON(ctx context.Context, serviceAccountJSONPath string, m *metrics.Metrics, l *zap.Logger) (StorageX, error) {
	client, err := storage.NewClient(ctx, option.WithCredentialsFile(serviceAccountJSONPath))
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(serviceAccountJSONPath)
	if err != nil {
		return nil, err
	}

	var serviceAccount struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal(data, &serviceAccount); err != nil {
		return nil, err
	}

	secretAccessID := serviceAccount.ClientEmail
	privateKey := []byte(serviceAccount.PrivateKey)

	if secretAccessID == "" || len(privateKey) == 0 {
		return nil, errors.NewInternalServerError("Invalid service account JSON", fmt.Errorf("missing client_email or private_key"))
	}

	return &gcsStorage{
		client:         client,
		secretAccessID: secretAccessID,
		privateKey:     privateKey,
		logger:         l,
		m:              m,
	}, nil
}

func (g *gcsStorage) PutObject(ctx context.Context, bucket, key string, data io.Reader, options *PutOptions) error {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "GCS.PutObject")
	defer span.End()

	start := time.Now()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "gcs"),
	)

	wc := g.client.Bucket(bucket).Object(key).NewWriter(ctx)
	if options != nil {
		if options.ContentType != "" {
			wc.ContentType = options.ContentType
			span.SetAttributes(attribute.String("object.content_type", options.ContentType))
		}
		if options.Metadata != nil {
			wc.Metadata = options.Metadata
			span.SetAttributes(attribute.Int("metadata.count", len(options.Metadata)))
		}
	}

	bytesWritten, err := io.Copy(wc, data)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to write object data")

		// Record error metric
		g.recordOperationMetrics(ctx, "put", false, time.Since(start))

		return err
	}

	span.SetAttributes(attribute.Int64("bytes_written", bytesWritten))

	if err := wc.Close(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to close writer")

		// Record error metric
		g.recordOperationMetrics(ctx, "put", false, time.Since(start))

		return err
	}

	// Record success metrics
	g.recordOperationMetrics(ctx, "put", true, time.Since(start))

	span.SetStatus(codes.Ok, "Object uploaded successfully")
	return nil
}

func (g *gcsStorage) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "GCS.GetObject")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "gcs"),
	)

	rc, err := g.client.Bucket(bucket).Object(key).NewReader(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to get object")
		return nil, err
	}

	span.SetStatus(codes.Ok, "Object reader created")
	return rc, nil
}

func (g *gcsStorage) GetObjectAttrs(ctx context.Context, bucket, key string) (*storage.ObjectAttrs, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "GCS.GetObjectAttrs")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "gcs"),
	)

	attrs, err := g.client.Bucket(bucket).Object(key).Attrs(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to get object attributes")
		return nil, err
	}

	span.SetAttributes(
		attribute.Int64("object.size", attrs.Size),
		attribute.String("object.content_type", attrs.ContentType),
	)
	span.SetStatus(codes.Ok, "Object attributes retrieved")
	return attrs, nil
}

func (g *gcsStorage) Close() error {
	return g.client.Close()
}

func (g *gcsStorage) GeneratePresignedURL(ctx context.Context, bucket, key string, options *PresignedURLOptions) (string, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "GCS.GeneratePresignedURL")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "gcs"),
	)

	if options == nil {
		options = &PresignedURLOptions{}
	}

	if options.Method != "" {
		span.SetAttributes(attribute.String("presigned.method", options.Method))
	}
	if options.ContentType != "" {
		span.SetAttributes(attribute.String("presigned.content_type", options.ContentType))
	}

	if g.secretAccessID == "" || len(g.privateKey) == 0 {
		err := errors.NewInternalServerError("GCS signing credentials are not configured", fmt.Errorf("missing GCS signing credentials"))
		span.RecordError(err)
		span.SetStatus(codes.Error, "Missing GCS signing credentials")
		return "", err
	}

	expiresIn := time.Duration(options.ExpiresInSeconds) * time.Second
	if expiresIn == 0 {
		expiresIn = 15 * time.Minute
	}
	expiresAt := time.Now().Add(expiresIn)

	span.SetAttributes(
		attribute.Int64("presigned.expires_in_seconds", int64(expiresIn.Seconds())),
		attribute.String("presigned.expires_at", expiresAt.Format(time.RFC3339)),
	)

	g.logger.Debug("Generating signed URL", zap.String("bucket", bucket), zap.String("key", key), zap.String("method", options.Method), zap.Time("expires_at", expiresAt))

	signedOpts := &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		GoogleAccessID: g.secretAccessID,
		PrivateKey:     g.privateKey,
		Method:         options.Method,
		Expires:        expiresAt,
	}

	if options.ContentType != "" {
		signedOpts.ContentType = options.ContentType
	}

	url, err := storage.SignedURL(bucket, key, signedOpts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to generate signed URL")
		return "", errors.NewInternalServerError("Failed to generate signed URL", err)
	}

	span.SetStatus(codes.Ok, "Presigned URL generated")
	return url, nil
}

func (g *gcsStorage) PublicURL(ctx context.Context, bucket, key string) (string, error) {
	tracer := otel.Tracer("mpiper-api")
	_, span := tracer.Start(ctx, "GCS.PublicURL")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "gcs"),
	)

	url := "https://storage.googleapis.com/" + bucket + "/" + key
	span.SetStatus(codes.Ok, "Public URL generated")
	return url, nil
}

func (g *gcsStorage) DeleteObject(ctx context.Context, bucket, key string) error {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "GCS.DeleteObject")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "gcs"),
	)

	err := g.client.Bucket(bucket).Object(key).Delete(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to delete object")
		return err
	}

	span.SetStatus(codes.Ok, "Object deleted successfully")
	return nil
}

// recordOperationMetrics records metrics for storage operations, including success/failure counts and operation duration.
func (g *gcsStorage) recordOperationMetrics(ctx context.Context, operation string, success bool, duration time.Duration) {
	if g.m == nil {
		return
	}
	status := "success"
	if !success {
		status = "error"
	}
	attrs := []attribute.KeyValue{
		attribute.String("storage.operation", operation),
		attribute.String("storage.provider", "gcs"),
		attribute.String("storage.status", status),
	}
	if success {
		g.m.StorageOperationTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	} else {
		g.m.StorageOperationErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	g.m.StorageOperationDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}
