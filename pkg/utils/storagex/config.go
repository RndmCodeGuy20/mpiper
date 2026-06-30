package storagex

type Provider string

const (
	GCPProvider   Provider = "gcp"
	GCSProvider   Provider = "gcs"
	S3Provider    Provider = "s3"
	MinIOProvider Provider = "minio"
)

type Config struct {
	Provider Provider

	// Common settings
	Region          string
	Endpoint        string // Internal/server-side endpoint (e.g., http://minio:9000)
	PublicEndpoint  string // Optional client-facing endpoint used for presigned + public URLs (e.g., http://localhost:9000). Falls back to Endpoint when empty.
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // Optional, for temporary credentials

	// GCP specific settings
	GCPProjectID      string
	GCPServiceAccount string
	GCPPrivateKeyPath string
}
