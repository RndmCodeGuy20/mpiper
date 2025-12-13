package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/queue"
	lredis "github.com/rndmcodeguy20/mpiper/internal/queue"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"github.com/rndmcodeguy20/mpiper/pkg/utils/storagex"
	"go.uber.org/zap"
)

type AssetService interface {
	CreateAsset(ctx context.Context, request models.UploadAssetRequest) (*models.UploadAssetResponse, error)
	MarkAssetUploaded(ctx context.Context, assetID uuid.UUID) error
}

type assetService struct {
	assetRepo     repository.AssetRepository
	logger        *utils.Logger
	storageClient storagex.StorageX
	queue         *queue.RedisQueue
}

func NewAssetService(redisCfg *config.RedisConfig, provider storagex.Provider, assetRepo repository.AssetRepository, logger *utils.Logger) AssetService {
	var storageClient storagex.StorageX
	var err error
	ctx := context.Background()
	switch provider {
	//case storagex.AWSProvider:
	//	storageClient = storagex.NewAWSStorageX()
	case storagex.GCPProvider:
		storageClient, err = storagex.NewGCSStorageFromServiceAccountJSON(ctx, ".secrets/aion-staging-d4d9b9c808ec.json")
	case storagex.AzureProvider:
		//storageClient = storagex.NewAzureStorageX()
	default:
		logger.Sugar().Fatalf("Unsupported storage provider: %v", provider)
	}

	if err != nil {
		logger.Sugar().Fatalf("Failed to initialize storage client: %v", err)
	}

	rc, err := lredis.MustGetRedisClient(redisCfg, logger)
	rq := lredis.NewRedisQueue(ctx, rc, lredis.RedisQueueOptions{
		QueueName:         "media:jobs",
		ConnectionTimeOut: 2 * time.Second,
		MaxStreamLength:   10_000,
		MaxRetries:        3,
		RetryInterval:     2 * time.Second,
		EnableMetrics:     true,
	}, logger)

	return &assetService{
		assetRepo:     assetRepo,
		logger:        logger,
		storageClient: storageClient,
		queue:         rq,
	}
}

func (s *assetService) CreateAsset(ctx context.Context, request models.UploadAssetRequest) (*models.UploadAssetResponse, error) {
	// create signedUrl
	assetID := uuid.New()
	objectKey := fmt.Sprintf("media/raw/%s", assetID)
	signedUrl, err := s.storageClient.GeneratePresignedURL(ctx, "mpiper", objectKey, &storagex.PresignedURLOptions{
		Method:           "PUT",
		ContentType:      request.ContentType,
		ExpiresInSeconds: 60 * 5, // 5 minutes
	})

	s.logger.Debug("Generated signed URL: ", zap.String("url", signedUrl))

	if err != nil {
		s.logger.Sugar().Errorf("Failed to generate presigned URL: %v", err)
		return nil, err
	}

	publicUrl, err := s.storageClient.PublicURL(ctx, "mpiper", objectKey)
	if err != nil {
		s.logger.Sugar().Errorf("Failed to get public URL: %v", err)
		return nil, err
	}
	s.logger.Debug("Public URL: ", zap.String("url", publicUrl))

	err = s.assetRepo.CreateAsset(assetID, publicUrl, request.Size, repository.ToAssetTypeFromMimeType(request.ContentType), request.ContentType)
	if err != nil {
		s.logger.Sugar().Errorf("Failed to create asset: %v", err)
		return nil, err
	}

	return &models.UploadAssetResponse{
		UploadUrl:  signedUrl,
		AssetID:    assetID.String(),
		Method:     "PUT",
		Headers:    map[string]string{"Content-Type": request.ContentType},
		ObjectPath: request.FileName,
		PublicUrl:  publicUrl,
		ExpiresAt:  60 * 5, // 5 minutes
	}, nil
}

func (s *assetService) MarkAssetUploaded(ctx context.Context, assetID uuid.UUID) error {
	// check if asset exists
	objectKey := fmt.Sprintf("media/raw/%s", assetID)
	_, err := s.storageClient.GetObjectAttrs(ctx, "mpiper", objectKey)

	if err != nil {
		s.logger.Sugar().Errorf("Failed to get object attrs: %v", err)
		return err
	}

	tx, err := s.assetRepo.GetDB().BeginTx(ctx, nil)
	defer func() {
		if tx != nil {
			if err := tx.Rollback(); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Sugar().Errorf("Failed to rollback transaction: %v", err)
			}
		}
	}()

	if err != nil {
		s.logger.Sugar().Errorf("Failed to begin transaction: %v", err)
		return err
	}

	changed, err := s.assetRepo.MarkAssetUploadedTx(ctx, tx, assetID)
	if err != nil {
		return err
	}

	if !changed {
		s.logger.Sugar().Infof("Asset %s already marked as uploaded", assetID)
		return nil
	}

	jobID, err := s.assetRepo.InsertProcessAssetJobTx(ctx, tx, assetID)
	if err != nil {
		s.logger.Sugar().Errorf("Failed to insert process asset job: %v", err)
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}
	tx = nil // Prevent deferred rollback after commit

	_, err = s.queue.Enqueue(ctx, map[string]interface{}{
		"job_id":    *jobID,
		"asset_id":  assetID.String(),
		"event":     "asset_uploaded",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})

	if err != nil {
		return err
	}

	return nil
}
