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
	Endpoint        string // For custom endpoints (e.g., MinIO)
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string // Optional, for temporary credentials

	// GCP specific settings
	GCPProjectID      string
	GCPServiceAccount string
	GCPPrivateKeyPath string
}
