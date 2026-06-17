package storagex

import (
	"context"
	"fmt"
	"strings"

	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"go.uber.org/zap"
)

// New builds a StorageX for the configured provider. "gcs"/"gcp" use the GCS
// service-account client; "s3"/"minio" use the S3 client (MinIO when an
// endpoint is set).
func New(ctx context.Context, cfg Config, m *metrics.Metrics, logger *zap.Logger) (StorageX, error) {
	switch Provider(strings.ToLower(string(cfg.Provider))) {
	case GCSProvider, GCPProvider:
		if cfg.GCPServiceAccount == "" {
			return nil, fmt.Errorf("GCS service account path (GCS_SA_PATH) is not set")
		}
		return NewGCSStorageFromServiceAccountJSON(ctx, cfg.GCPServiceAccount, m, logger)
	case S3Provider, MinIOProvider:
		return NewS3Storage(ctx, cfg, m, logger)
	default:
		return nil, fmt.Errorf("unknown storage provider: %q", cfg.Provider)
	}
}
