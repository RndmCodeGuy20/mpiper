package storagex

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

type s3Storage struct {
	client   *s3.Client
	presign  *s3.PresignClient
	region   string
	endpoint string // non-empty for MinIO / S3-compatible endpoints
	logger   *zap.Logger
	m        *metrics.Metrics
}

// NewS3Storage builds an S3-backed StorageX. An empty cfg.Endpoint targets AWS
// S3; a non-empty one (with path-style addressing) targets MinIO or any
// S3-compatible store.
func NewS3Storage(ctx context.Context, cfg Config, m *metrics.Metrics, logger *zap.Logger) (StorageX, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if cfg.AccessKeyID != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, errors.NewInternalServerError("Failed to load AWS config", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // MinIO and most S3-compatible stores need path-style
		}
	})

	return &s3Storage{
		client:   client,
		presign:  s3.NewPresignClient(client),
		region:   region,
		endpoint: cfg.Endpoint,
		logger:   logger,
		m:        m,
	}, nil
}

func (s *s3Storage) PutObject(ctx context.Context, bucket, key string, data io.Reader, options *PutOptions) error {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "S3.PutObject")
	defer span.End()

	start := time.Now()
	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "s3"),
	)

	in := &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: data}
	if options != nil {
		if options.ContentType != "" {
			in.ContentType = aws.String(options.ContentType)
		}
		if options.Metadata != nil {
			in.Metadata = options.Metadata
		}
	}

	if _, err := s.client.PutObject(ctx, in); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to put object")
		s.recordOperationMetrics(ctx, "put", false, time.Since(start))
		return err
	}

	s.recordOperationMetrics(ctx, "put", true, time.Since(start))
	span.SetStatus(codes.Ok, "Object uploaded successfully")
	return nil
}

func (s *s3Storage) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "S3.GetObject")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "s3"),
	)

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to get object")
		return nil, err
	}

	span.SetStatus(codes.Ok, "Object reader created")
	return out.Body, nil
}

func (s *s3Storage) GetObjectAttrs(ctx context.Context, bucket, key string) (*ObjectAttrs, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "S3.GetObjectAttrs")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "s3"),
	)

	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to get object attributes")
		return nil, err
	}

	attrs := &ObjectAttrs{
		Size:        aws.ToInt64(head.ContentLength),
		ContentType: aws.ToString(head.ContentType),
		ETag:        aws.ToString(head.ETag),
	}
	span.SetAttributes(
		attribute.Int64("object.size", attrs.Size),
		attribute.String("object.content_type", attrs.ContentType),
	)
	span.SetStatus(codes.Ok, "Object attributes retrieved")
	return attrs, nil
}

func (s *s3Storage) GeneratePresignedURL(ctx context.Context, bucket, key string, options *PresignedURLOptions) (string, error) {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "S3.GeneratePresignedURL")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "s3"),
	)

	if options == nil {
		options = &PresignedURLOptions{}
	}

	expiresIn := time.Duration(options.ExpiresInSeconds) * time.Second
	if expiresIn == 0 {
		expiresIn = 15 * time.Minute
	}
	withExpiry := s3.WithPresignExpires(expiresIn)

	var (
		url string
		err error
	)
	switch strings.ToUpper(options.Method) {
	case "", "PUT":
		in := &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
		if options.ContentType != "" {
			in.ContentType = aws.String(options.ContentType)
		}
		var req *v4.PresignedHTTPRequest
		if req, err = s.presign.PresignPutObject(ctx, in, withExpiry); req != nil {
			url = req.URL
		}
	case "GET":
		var req *v4.PresignedHTTPRequest
		if req, err = s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}, withExpiry); req != nil {
			url = req.URL
		}
	default:
		return "", errors.NewInternalServerError("Unsupported presign method", fmt.Errorf("method %q", options.Method))
	}

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to generate presigned URL")
		return "", errors.NewInternalServerError("Failed to generate presigned URL", err)
	}

	span.SetStatus(codes.Ok, "Presigned URL generated")
	return url, nil
}

func (s *s3Storage) PublicURL(ctx context.Context, bucket, key string) (string, error) {
	_, span := otel.Tracer("mpiper-api").Start(ctx, "S3.PublicURL")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "s3"),
	)

	var url string
	if s.endpoint != "" {
		// path-style for MinIO / S3-compatible endpoints
		url = fmt.Sprintf("%s/%s/%s", strings.TrimRight(s.endpoint, "/"), bucket, key)
	} else {
		url = fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, s.region, key)
	}
	span.SetStatus(codes.Ok, "Public URL generated")
	return url, nil
}

func (s *s3Storage) DeleteObject(ctx context.Context, bucket, key string) error {
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "S3.DeleteObject")
	defer span.End()

	span.SetAttributes(
		attribute.String("storage.bucket", bucket),
		attribute.String("storage.key", key),
		attribute.String("storage.provider", "s3"),
	)

	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to delete object")
		return err
	}

	span.SetStatus(codes.Ok, "Object deleted successfully")
	return nil
}

func (s *s3Storage) recordOperationMetrics(ctx context.Context, operation string, success bool, duration time.Duration) {
	if s.m == nil {
		return
	}
	status := "success"
	if !success {
		status = "error"
	}
	attrs := []attribute.KeyValue{
		attribute.String("storage.operation", operation),
		attribute.String("storage.provider", "s3"),
		attribute.String("storage.status", status),
	}
	if success {
		s.m.StorageOperationTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	} else {
		s.m.StorageOperationErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	s.m.StorageOperationDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}
