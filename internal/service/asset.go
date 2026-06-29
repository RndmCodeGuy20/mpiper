package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rndmcodeguy20/mpiper/internal/config"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/middleware"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
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
	outboxRepo    repository.OutboxRepository
	logger        *zap.Logger
	storageClient storagex.StorageX
	bucket        string
	m             *metrics.Metrics
}

func NewAssetService(assetRepo repository.AssetRepository, outboxRepo repository.OutboxRepository, logger *zap.Logger, m *metrics.Metrics) AssetService {
	ctx := context.Background()
	storeCfg := config.MustGet().Storage

	// Effective bucket: S3 may override via S3_BUCKET_NAME, otherwise BUCKET_NAME.
	bucket := storeCfg.Bucket
	switch storagex.Provider(strings.ToLower(storeCfg.Provider)) {
	case storagex.S3Provider, storagex.MinIOProvider:
		bucket = storeCfg.S3.Bucket
	}

	storageClient, err := storagex.New(ctx, storagex.Config{
		Provider:          storagex.Provider(storeCfg.Provider),
		Bucket:            bucket,
		Region:            storeCfg.S3.Region,
		Endpoint:          storeCfg.S3.EndpointURL,
		AccessKeyID:       storeCfg.S3.AccessKeyID,
		SecretAccessKey:   storeCfg.S3.SecretAccessKey,
		GCPServiceAccount: storeCfg.GCS.SAPath,
	}, m, logger)
	if err != nil {
		logger.Sugar().Fatalf("Failed to initialize storage client: %v", err)
	}

	return &assetService{
		assetRepo:     assetRepo,
		outboxRepo:    outboxRepo,
		logger:        logger,
		storageClient: storageClient,
		bucket:        bucket,
		m:             m,
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
	signedUrl, err := s.storageClient.GeneratePresignedURL(spanStorageCtx, s.bucket, objectKey, &storagex.PresignedURLOptions{
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
	publicUrl, err := s.storageClient.PublicURL(spanStorageCtx, s.bucket, objectKey)
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
	ownerID, _ := middleware.GetUserID(ctx)
	err = s.assetRepo.CreateAsset(spanStorageCtx, assetID, publicUrl, request.Size, repository.ToAssetTypeFromMimeType(request.ContentType), request.ContentType, ownerID)
	spanStorage.End()

	if err != nil {
		spanStorage.RecordError(err)

		// Record failure metric
		if s.m != nil {
			attrs := []attribute.KeyValue{
				attribute.String("status", "error"),
				attribute.String("error.type", "db_error"),
			}
			s.m.AssetUploadTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		}

		return nil, err
	}

	duration := time.Since(start).Seconds()
	attrs := []attribute.KeyValue{
		attribute.String("status", "success"),
		attribute.String("asset_type", string(repository.ToAssetTypeFromMimeType(request.ContentType))),
	}

	if s.m != nil {
		s.m.AssetUploadTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		s.m.AssetUploadDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
		s.m.AssetSizeBytes.Record(ctx, request.Size, metric.WithAttributes(attrs...))
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
	_, err := s.storageClient.GetObjectAttrs(ctxStorage, s.bucket, objectKey)
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

	// Insert outbox row in the same transaction — atomic with job + asset status.
	ctxOutbox, spanOutbox := tracer.Start(ctxTx, "OutboxRepo.InsertTx")
	spanOutbox.SetAttributes(attribute.String("asset_id", assetID.String()), attribute.Int64("job_id", *jobID))
	payload, _ := json.Marshal(map[string]interface{}{
		"job_id":    *jobID,
		"asset_id":  assetID.String(),
		"event":     "asset_uploaded",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	err = s.outboxRepo.InsertTx(ctxOutbox, tx, models.OutboxEvent{
		AggregateID: assetID,
		JobID:       jobID,
		Event:       "asset_uploaded",
		Payload:     payload,
	})
	spanOutbox.End()

	if err != nil {
		spanOutbox.RecordError(err)
		spanOutbox.SetStatus(codes.Error, "Failed to insert outbox event")
		span.RecordError(err)
		span.SetStatus(codes.Error, "Outbox insert failed")
		s.logger.Sugar().Errorf("Failed to insert outbox event: %v", err)
		return err
	}

	// Insert job.starting webhook deliveries for matching registrations (same tx).
	webhookPayload, _ := json.Marshal(map[string]interface{}{
		"event":    "job.starting",
		"asset_id": assetID.String(),
		"job_id":   *jobID,
		"status":   "starting",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	_, _ = tx.ExecContext(ctxTx,
		`INSERT INTO webhook_deliveries (registration_id, event, asset_id, job_id, payload)
		 SELECT wr.id, 'job.starting', $1, $2, $3::jsonb
		 FROM webhook_registrations wr
		 JOIN assets a ON a.owner_id = wr.user_id
		 WHERE a.asset_id = $1 AND wr.events @> '["job.starting"]'::jsonb`,
		assetID, *jobID, webhookPayload,
	)

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

	span.SetStatus(codes.Ok, "Asset marked as uploaded and outbox event created")
	return nil
}
