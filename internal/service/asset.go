package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/queue"
	lredis "github.com/rndmcodeguy20/mpiper/internal/queue"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"github.com/rndmcodeguy20/mpiper/pkg/utils/storagex"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
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
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "AssetService.CreateAsset")
	defer span.End()

	start := time.Now()

	// create signedUrl
	assetID := uuid.New()
	span.SetAttributes(
		attribute.String("asset_id", assetID.String()),
		attribute.String("content_type", request.ContentType),
		attribute.Int64("content_length", request.Size),
	)

	objectKey := fmt.Sprintf("media/raw/%s", assetID)

	spanStorageCtx, spanStorage := tracer.Start(ctx, "StorageClient.GeneratePresignedURL")
	spanStorage.SetAttributes(attribute.String("object_key", objectKey))
	// GeneratePresignedURL creates a temporary signed URL for uploading an object to the storage bucket.
	// It generates a PUT presigned URL valid for 5 minutes that allows clients to upload files
	// with the specified content type to the "mpiper" bucket at the given object key.
	signedUrl, err := s.storageClient.GeneratePresignedURL(spanStorageCtx, "mpiper", objectKey, &storagex.PresignedURLOptions{
		Method:           "PUT",
		ContentType:      request.ContentType,
		ExpiresInSeconds: 60 * 5, // 5 minutes
	})
	spanStorage.End()

	s.logger.Debug("Generated signed URL: ", zap.String("url", signedUrl))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to generate presigned URL")
		s.logger.Sugar().Errorf("Failed to generate presigned URL: %v", err)
		return nil, err
	}

	spanStorageCtx, spanStorage = tracer.Start(ctx, "StorageClient.PublicURL")
	spanStorage.SetAttributes(attribute.String("object_key", objectKey))
	publicUrl, err := s.storageClient.PublicURL(spanStorageCtx, "mpiper", objectKey)
	spanStorage.End()

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to get public URL")
		s.logger.Sugar().Errorf("Failed to get public URL: %v", err)
		return nil, err
	}
	s.logger.Debug("Public URL: ", zap.String("url", publicUrl))

	spanStorageCtx, spanStorage = tracer.Start(ctx, "AssetRepo.CreateAsset")
	spanStorage.SetAttributes(attribute.String("asset_id", assetID.String()))
	err = s.assetRepo.CreateAsset(spanStorageCtx, assetID, publicUrl, request.Size, repository.ToAssetTypeFromMimeType(request.ContentType), request.ContentType)
	spanStorage.End()

	if err != nil {
		spanStorage.RecordError(err)

		// Record failure metric
		if metrics.AssetUploadTotal != nil {
			attrs := []attribute.KeyValue{
				attribute.String("status", "error"),
				attribute.String("error.type", "db_error"),
			}
			metrics.AssetUploadTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		}

		return nil, err
	}

	// Record successful asset creation
	duration := time.Since(start).Seconds()
	attrs := []attribute.KeyValue{
		attribute.String("status", "success"),
		attribute.String("asset_type", string(repository.ToAssetTypeFromMimeType(request.ContentType))),
	}

	if metrics.AssetUploadTotal != nil {
		metrics.AssetUploadTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	}

	if metrics.AssetUploadDuration != nil {
		metrics.AssetUploadDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
	}

	if metrics.AssetSizeBytes != nil {
		metrics.AssetSizeBytes.Record(ctx, request.Size, metric.WithAttributes(attrs...))
	}

	span.SetStatus(codes.Ok, "Asset created successfully")

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
	tracer := otel.Tracer("mpiper-api")
	ctx, span := tracer.Start(ctx, "AssetService.MarkAssetUploaded")
	defer span.End()

	span.SetAttributes(attribute.String("asset_id", assetID.String()))

	// check if asset exists
	objectKey := fmt.Sprintf("media/raw/%s", assetID)
	span.SetAttributes(attribute.String("object_key", objectKey))

	ctxStorage, spanStorage := tracer.Start(ctx, "StorageClient.GetObjectAttrs")
	spanStorage.SetAttributes(attribute.String("object_key", objectKey))
	_, err := s.storageClient.GetObjectAttrs(ctxStorage, "mpiper", objectKey)
	spanStorage.End()

	if err != nil {
		spanStorage.RecordError(err)
		spanStorage.SetStatus(codes.Error, "Object not found in storage")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to verify object in storage")
		s.logger.Sugar().Errorf("Failed to get object attrs: %v", err)
		return err
	}

	ctxTx, spanTx := tracer.Start(ctx, "Database.Transaction")
	tx, err := s.assetRepo.GetDB().BeginTx(ctxTx, nil)
	defer func() {
		if tx != nil {
			if err := tx.Rollback(); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Sugar().Errorf("Failed to rollback transaction: %v", err)
			}
		}
		spanTx.End()
	}()

	if err != nil {
		spanTx.RecordError(err)
		spanTx.SetStatus(codes.Error, "Failed to begin transaction")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Transaction initialization failed")
		s.logger.Sugar().Errorf("Failed to begin transaction: %v", err)
		return err
	}

	ctxUpdate, spanUpdate := tracer.Start(ctxTx, "AssetRepo.MarkAssetUploadedTx")
	spanUpdate.SetAttributes(attribute.String("asset_id", assetID.String()))
	changed, err := s.assetRepo.MarkAssetUploadedTx(ctxUpdate, tx, assetID)
	spanUpdate.End()

	if err != nil {
		spanUpdate.RecordError(err)
		spanUpdate.SetStatus(codes.Error, "Failed to mark asset as uploaded")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Database update failed")
		return err
	}

	if !changed {
		spanUpdate.AddEvent("Asset already uploaded")
		span.AddEvent("Asset already in uploaded state")
		s.logger.Sugar().Infof("Asset %s already marked as uploaded", assetID)
		return nil
	}

	ctxJob, spanJob := tracer.Start(ctxTx, "AssetRepo.InsertProcessAssetJobTx")
	spanJob.SetAttributes(attribute.String("asset_id", assetID.String()))
	jobID, err := s.assetRepo.InsertProcessAssetJobTx(ctxJob, tx, assetID)
	spanJob.End()

	if err != nil {
		spanJob.RecordError(err)
		spanJob.SetStatus(codes.Error, "Failed to create processing job")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Job creation failed")
		s.logger.Sugar().Errorf("Failed to insert process asset job: %v", err)
		return err
	}

	spanJob.SetAttributes(attribute.Int64("job_id", *jobID))

	err = tx.Commit()
	if err != nil {
		spanTx.RecordError(err)
		spanTx.SetStatus(codes.Error, "Transaction commit failed")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to commit transaction")
		return err
	}
	tx = nil // Prevent deferred rollback after commit
	spanTx.SetStatus(codes.Ok, "Transaction committed")

	ctxQueue, spanQueue := tracer.Start(ctx, "Queue.Enqueue")
	spanQueue.SetAttributes(
		attribute.Int64("job_id", *jobID),
		attribute.String("asset_id", assetID.String()),
		attribute.String("event", "asset_uploaded"),
	)
	_, err = s.queue.Enqueue(ctxQueue, map[string]interface{}{
		"job_id":    *jobID,
		"asset_id":  assetID.String(),
		"event":     "asset_uploaded",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	spanQueue.End()

	if err != nil {
		spanQueue.RecordError(err)
		spanQueue.SetStatus(codes.Error, "Failed to enqueue job")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Queue enqueue failed")
		return err
	}

	spanQueue.SetStatus(codes.Ok, "Job enqueued successfully")
	span.SetStatus(codes.Ok, "Asset marked as uploaded and job queued")
	return nil
}
