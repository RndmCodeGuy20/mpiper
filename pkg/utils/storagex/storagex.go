package storagex

import (
	"context"
	"io"
	"time"

	"cloud.google.com/go/storage"
)

type PutOptions struct {
	Metadata    map[string]string
	ContentType string
	// Add other options as needed: ACL, StorageClass, etc.
}

type PresignedURLOptions struct {
	ContentType      string
	ExpiresInSeconds int64
	Method           string // e.g., "PUT", "GET"
	// Add other options as needed: Headers, QueryParams, etc.
}

type StorageX interface {
	PutObject(ctx context.Context, bucket, key string, data io.Reader, options *PutOptions) error
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	GetObjectAttrs(ctx context.Context, bucket, key string) (*storage.ObjectAttrs, error)
	GeneratePresignedURL(ctx context.Context, bucket, key string, options *PresignedURLOptions) (string, error)
	PublicURL(ctx context.Context, bucket, key string) (string, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	recordOperationMetrics(ctx context.Context, operation string, success bool, durationSeconds time.Duration)
}
