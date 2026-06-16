package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	appErrors "github.com/rndmcodeguy20/mpiper/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

type AssetType string

const (
	ImageAsset    AssetType = "image"
	VideoAsset    AssetType = "video"
	AudioAsset    AssetType = "audio"
	DocumentAsset AssetType = "document"
	OtherAsset    AssetType = "other"
)

type Status string

const (
	StatusUploading  Status = "uploading"
	StatusUploaded   Status = "uploaded"
	StatusProcessing Status = "processing"
	StatusReady      Status = "done"
	StatusFailed     Status = "failed"
)

func ToAssetType(fileType string) AssetType {
	switch fileType {
	case "image":
		return ImageAsset
	case "video":
		return VideoAsset
	case "audio":
		return AudioAsset
	case "document":
		return DocumentAsset
	default:
		return OtherAsset
	}
}

// SupportedMIMETypes is the single source of truth for which content types the
// pipeline accepts for upload and processing, mapped to their asset category.
// The handler gates uploads against this set; do not maintain a second list.
var SupportedMIMETypes = map[string]AssetType{
	"image/jpeg":      ImageAsset,
	"image/png":       ImageAsset,
	"image/webp":      ImageAsset,
	"video/mp4":       VideoAsset,
	"video/quicktime": VideoAsset,
}

// IsSupportedMIMEType reports whether the pipeline accepts the given content type.
func IsSupportedMIMEType(mimeType string) bool {
	_, ok := SupportedMIMETypes[mimeType]
	return ok
}

func ToAssetTypeFromMimeType(mimeType string) AssetType {
	if len(mimeType) < 5 {
		return OtherAsset
	}
	switch {
	case mimeType[0:5] == "image":
		return ImageAsset
	case mimeType[0:5] == "video":
		return VideoAsset
	case mimeType[0:5] == "audio":
		return AudioAsset
	case mimeType == "application/pdf" || mimeType == "application/msword" ||
		mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return DocumentAsset
	default:
		return OtherAsset
	}
}

type AssetRepository interface {
	CreateAsset(ictx context.Context, d uuid.UUID, url string, size int64, fileType AssetType, mimeType string) error
	CreateAssetTx(ctx context.Context, tx *sql.Tx, id uuid.UUID, url string, size int64, fileType AssetType, mimeType string) error
	MarkAssetUploadedTx(ctx context.Context, tx *sql.Tx, id uuid.UUID) (bool, error)
	InsertProcessAssetJobTx(ctx context.Context, tx *sql.Tx, assetID uuid.UUID) (*int64, error)
	GetDB() *sqlx.DB
}

type assetRepo struct {
	db     *sqlx.DB
	logger *zap.Logger
	m      *metrics.Metrics
}

func NewAssetRepository(db *sqlx.DB, logger *zap.Logger, m *metrics.Metrics) AssetRepository {
	return &assetRepo{db: db, logger: logger, m: m}
}

func (r *assetRepo) GetDB() *sqlx.DB {
	return r.db
}

func (r *assetRepo) CreateAsset(ctx context.Context, id uuid.UUID, url string, size int64, fileType AssetType, mimeType string) error {
	start := time.Now()
	query := `INSERT INTO assets (asset_id, original_url, type, mime_type, status, size_bytes) VALUES ($1, $2, $3, $4, $5, $6);`
	_, err := r.db.ExecContext(
		ctx,
		query,
		id,
		url,
		fileType,
		mimeType,
		StatusUploading,
		size,
	)

	// Record database metrics
	duration := time.Since(start).Seconds()
	attrs := []attribute.KeyValue{
		attribute.String("db.operation", "insert"),
		attribute.String("db.table", "assets"),
	}

	if err != nil {
		attrs = append(attrs, attribute.String("db.status", "error"))
		if r.m != nil {
			r.m.DBQueryErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
		}
		r.logger.Sugar().Errorf("Failed to create asset: %v", err)
		return appErrors.NewInternalServerError("Could not insert row", err)
	}

	attrs = append(attrs, attribute.String("db.status", "success"))
	if r.m != nil {
		r.m.DBQueryTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		r.m.DBQueryDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
	}

	return nil
}

func (r *assetRepo) CreateAssetTx(ctx context.Context, tx *sql.Tx, id uuid.UUID, url string, size int64, fileType AssetType, mimeType string) error {
	start := time.Now()
	query := `INSERT INTO assets (asset_id, original_url, type, mime_type, status, size_bytes) VALUES ($1, $2, $3, $4, $5, $6);`
	_, err := tx.ExecContext(
		ctx,
		query,
		id,
		url,
		fileType,
		mimeType,
		StatusUploading,
		size,
	)

	// Record database metrics
	duration := time.Since(start).Seconds()
	attrs := []attribute.KeyValue{
		attribute.String("db.operation", "insert"),
		attribute.String("db.table", "assets"),
	}

	if err != nil {
		attrs = append(attrs, attribute.String("db.status", "error"))
		if r.m != nil {
			r.m.DBQueryErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
		}
		r.logger.Sugar().Errorf("Failed to create asset in transaction: %v", err)
		return appErrors.NewInternalServerError("Could not insert row in transaction", err)
	}

	attrs = append(attrs, attribute.String("db.status", "success"))
	if r.m != nil {
		r.m.DBQueryTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
		r.m.DBQueryDuration.Record(ctx, duration, metric.WithAttributes(attrs...))
	}

	return nil
}

func (r *assetRepo) MarkAssetUploadedTx(ctx context.Context, tx *sql.Tx, id uuid.UUID) (bool, error) {
	query := `UPDATE assets SET status = $1, updated_at = NOW() WHERE asset_id = $2 AND status = 'uploading';`
	res, err := tx.ExecContext(
		ctx,
		query,
		StatusUploaded,
		id,
	)
	if err != nil {
		r.logger.Sugar().Errorf("Failed to mark asset as uploaded in transaction: %v", err)
		return false, appErrors.NewInternalServerError("Could not update row in transaction", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		r.logger.Sugar().Errorf("Failed to get rows affected: %v", err)
		return false, appErrors.NewInternalServerError("Could not get rows affected in transaction", err)
	}

	if rowsAffected == 0 {
		return false, nil // No rows updated, asset might not be in 'uploading' state
	}
	return true, nil // Asset marked as uploaded successfully
}

func (r *assetRepo) InsertProcessAssetJobTx(ctx context.Context, tx *sql.Tx, assetID uuid.UUID) (*int64, error) {
	var jobID int64
	query := `INSERT INTO jobs (type, asset_id, status)
				VALUES ($1, $2, $3)
				ON CONFLICT (asset_id, type) DO UPDATE SET updated_at = NOW()
				RETURNING job_id;`

	err := tx.QueryRowContext(
		ctx,
		query,
		"process_asset", // TODO: change to image_processing or video_processing based on asset type
		assetID,
		StatusProcessing,
	).Scan(&jobID)

	if err != nil {
		r.logger.Sugar().Errorf("Failed to insert process asset job in transaction: %v", err)
		return nil, appErrors.NewInternalServerError("Could not insert process asset job in transaction", err)
	}

	return &jobID, nil
}

func ExecTransaction(ctx context.Context, db *sqlx.DB, maxAttempts int, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	// small rng for jitter
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Respect context early
		if err := ctx.Err(); err != nil {
			return err
		}

		tx, err := db.BeginTx(ctx, opts)
		if err != nil {
			// If `begin` failed, check whether it's transient and retry accordingly.
			lastErr = err
			if shouldRetry(err) && attempt < maxAttempts {
				sleepWithBackoff(ctx, attempt, rng)
				continue
			}
			return fmt.Errorf("begin tx: %w", err)
		}

		// Ensure rollback on non-commit exit. If commit already happened, rollback returns `sql.ErrTxDone` which we ignore.
		rolledBack := false
		defer func() {
			// This deferred rollback runs when the function scope exits.
			// We only want to call rollback here if it hasn't been committed or already rolled back.
			if !rolledBack {
				_ = tx.Rollback()
			}
		}()

		// Catch panics from fn, rollback, and return an error.
		var fnErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					// convert panic to error
					fnErr = fmt.Errorf("panic in transaction func: %v", r)
				}
			}()

			fnErr = fn(tx)
		}()

		if fnErr != nil {
			// callback asked rollback (or panicked)
			// attempt rollback; prefer original fnErr if rollback succeeds
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				// combine errors but prioritize original fn error
				lastErr = fmt.Errorf("tx rollback error: %v (original: %w)", rbErr, fnErr)
			} else {
				lastErr = fnErr
			}
			rolledBack = true

			// If error is transient, and we have attempts left, retry
			if shouldRetry(fnErr) && attempt < maxAttempts && ctx.Err() == nil {
				sleepWithBackoff(ctx, attempt, rng)
				continue
			}
			return lastErr
		}

		// Try to commit
		if err := tx.Commit(); err != nil {
			lastErr = fmt.Errorf("tx commit: %w", err)
			// On commit error, rollback is usually automatic or tx is done; but try rollback for safety.
			_ = tx.Rollback()
			rolledBack = true

			// Retry on transient commit errors
			if shouldRetry(err) && attempt < maxAttempts && ctx.Err() == nil {
				sleepWithBackoff(ctx, attempt, rng)
				continue
			}
			return lastErr
		}

		// success
		rolledBack = true // prevent deferred rollback (tx already completed)
		return nil
	}

	if lastErr == nil {
		lastErr = errors.New("transaction failed after retries")
	}
	return lastErr
}

func sleepWithBackoff(ctx context.Context, attempt int, rng *rand.Rand) {
	// base 100-200ms, then exponential
	base := 100 * time.Millisecond
	// cap
	maxCap := 5 * time.Second

	// exponential backoff
	backoff := time.Duration(float64(base) * math.Pow(2, float64(attempt-1)))
	if backoff > maxCap {
		backoff = maxCap
	}

	// jitter +/-25%
	jitter := time.Duration(rng.Int63n(int64(backoff/2))) - (backoff / 4)
	sleep := backoff + jitter
	if sleep < 0 {
		sleep = 0
	}

	timer := time.NewTimer(sleep)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}

	// never retry canceled contexts or deadlines
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	// common Postgres serialization / deadlock SQLSTATE codes or message fragments
	// Postgres: 40001 = serialization_failure, 40P01 = deadlock_detected
	// MySQL: 1213 = deadlock
	// General messages:
	msg := strings.ToLower(err.Error())

	transientMarkers := []string{
		"sqlstate 40001",        // pq style
		"40001",                 // generic
		"serialization_failure", // textual
		"could not serialize",   // textual
		"sqlstate 40p01",
		"40p01",
		"deadlock detected",
		"deadlock",
		"lock wait timeout", // mysql
		"1213",              // mysql deadlock code
		"serialization error",
		"retry transaction",
		"write conflict",
	}

	for _, m := range transientMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
