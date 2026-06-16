package storagex

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.uber.org/zap"
)

// TestS3StorageRoundTrip exercises the S3 impl against a real S3-compatible
// store (MinIO). It is skipped unless S3_TEST_ENDPOINT is set, so it never
// runs in CI without an endpoint.
//
//	S3_TEST_ENDPOINT=http://localhost:9000 S3_TEST_ACCESS_KEY=... \
//	S3_TEST_SECRET_KEY=... S3_TEST_BUCKET=test-bucket go test ./pkg/utils/storagex/ -run TestS3
func TestS3StorageRoundTrip(t *testing.T) {
	endpoint := os.Getenv("S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set S3_TEST_ENDPOINT to run the MinIO integration test")
	}
	ak := os.Getenv("S3_TEST_ACCESS_KEY")
	sk := os.Getenv("S3_TEST_SECRET_KEY")
	bucket := os.Getenv("S3_TEST_BUCKET")
	ctx := context.Background()

	cfg := Config{
		Provider:        S3Provider,
		Region:          "us-east-1",
		Endpoint:        endpoint,
		Bucket:          bucket,
		AccessKeyID:     ak,
		SecretAccessKey: sk,
	}

	// Ensure the bucket exists (create via a raw client; ignore if it already does).
	createTestBucket(t, ctx, cfg, bucket)

	st, err := NewS3Storage(ctx, cfg, nil, zap.NewNop())
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	key := "test/roundtrip.txt"
	body := "hello mpiper s3"

	if err := st.PutObject(ctx, bucket, key, strings.NewReader(body), &PutOptions{ContentType: "text/plain"}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	attrs, err := st.GetObjectAttrs(ctx, bucket, key)
	if err != nil {
		t.Fatalf("GetObjectAttrs: %v", err)
	}
	if attrs.Size != int64(len(body)) {
		t.Errorf("size = %d, want %d", attrs.Size, len(body))
	}
	if attrs.ContentType != "text/plain" {
		t.Errorf("contentType = %q, want text/plain", attrs.ContentType)
	}

	rc, err := st.GetObject(ctx, bucket, key)
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}

	url, err := st.GeneratePresignedURL(ctx, bucket, key, &PresignedURLOptions{Method: "GET", ExpiresInSeconds: 60})
	if err != nil {
		t.Fatalf("GeneratePresignedURL: %v", err)
	}
	if !strings.Contains(url, key) || !strings.Contains(url, "X-Amz-Signature") {
		t.Errorf("presigned url looks wrong: %s", url)
	}

	if err := st.DeleteObject(ctx, bucket, key); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := st.GetObjectAttrs(ctx, bucket, key); err == nil {
		t.Error("GetObjectAttrs after delete: want error, got nil")
	}
}

func createTestBucket(t *testing.T, ctx context.Context, cfg Config, bucket string) {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
	})
	// Ignore error: bucket may already exist.
	_, _ = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
}
