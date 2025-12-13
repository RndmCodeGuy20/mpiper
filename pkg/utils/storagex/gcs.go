package storagex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"cloud.google.com/go/storage"
	"github.com/rndmcodeguy20/mpiper/pkg/errors"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
	"google.golang.org/api/option"
)

type gcsStorage struct {
	client         *storage.Client
	secretAccessID string
	privateKey     []byte
	logger         *utils.Logger
}

func NewGCSStorage(ctx context.Context, projectID string) (StorageX, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &gcsStorage{
		client: client,
	}, nil
}

func NewGCSStorageFromServiceAccountJSON(ctx context.Context, serviceAccountJSONPath string) (StorageX, error) {
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
		logger:         utils.NewLogger(),
	}, nil
}

func (g *gcsStorage) PutObject(ctx context.Context, bucket, key string, data io.Reader, options *PutOptions) error {
	wc := g.client.Bucket(bucket).Object(key).NewWriter(ctx)
	if options != nil {
		if options.ContentType != "" {
			wc.ContentType = options.ContentType
		}
		if options.Metadata != nil {
			wc.Metadata = options.Metadata
		}
	}
	if _, err := io.Copy(wc, data); err != nil {
		return err
	}
	return wc.Close()
}

func (g *gcsStorage) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	rc, err := g.client.Bucket(bucket).Object(key).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	return rc, nil
}

func (g *gcsStorage) GetObjectAttrs(ctx context.Context, bucket, key string) (*storage.ObjectAttrs, error) {
	attrs, err := g.client.Bucket(bucket).Object(key).Attrs(ctx)
	if err != nil {
		return nil, err
	}
	return attrs, nil
}

func (g *gcsStorage) Close() error {
	return g.client.Close()
}

func (g *gcsStorage) GeneratePresignedURL(ctx context.Context, bucket, key string, options *PresignedURLOptions) (string, error) {
	if options == nil {
		options = &PresignedURLOptions{}
	}

	if g.secretAccessID == "" || len(g.privateKey) == 0 {
		return "", errors.NewInternalServerError("GCS signing credentials are not configured", fmt.Errorf("missing GCS signing credentials"))
	}

	expiresIn := time.Duration(options.ExpiresInSeconds) * time.Second
	if expiresIn == 0 {
		expiresIn = 15 * time.Minute
	}
	expiresAt := time.Now().Add(expiresIn)

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
		return "", errors.NewInternalServerError("Failed to generate signed URL", err)
	}

	return url, nil
}

func (g *gcsStorage) PublicURL(ctx context.Context, bucket, key string) (string, error) {
	return "https://storage.googleapis.com/" + bucket + "/" + key, nil
}

func (g *gcsStorage) DeleteObject(ctx context.Context, bucket, key string) error {
	return g.client.Bucket(bucket).Object(key).Delete(ctx)
}
