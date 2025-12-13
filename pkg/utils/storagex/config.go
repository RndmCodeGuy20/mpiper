package storagex

type Provider string

const (
	AWSProvider   Provider = "aws"
	GCPProvider   Provider = "gcp"
	AzureProvider Provider = "azure"
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

	// AWS specific settings
	AWSUseIAMRole bool // If true, use IAM Role for authentication

	// GCP specific settings
	GCPProjectID      string
	GCPServiceAccount string
	GCPPrivateKeyPath string

	// Azure specific settings
	AzureAccountName string
	AzureAccountKey  string
	AzureContainer   string
}
