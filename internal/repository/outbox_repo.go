package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"go.uber.org/zap"
)

type OutboxRepository interface {
	InsertTx(ctx context.Context, tx *sql.Tx, event models.OutboxEvent) error
	FetchPendingBatch(ctx context.Context, limit int) ([]models.OutboxEvent, error)
	MarkPublished(ctx context.Context, ids []int64) error
	IncrementAttempts(ctx context.Context, id int64, errMsg string) error
	MarkFailed(ctx context.Context, id int64, errMsg string) error
	DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error)
	CountPending(ctx context.Context) (int64, error)
}

type outboxRepo struct {
	db     *sqlx.DB
	logger *zap.Logger
}

func NewOutboxRepository(db *sqlx.DB, logger *zap.Logger) OutboxRepository {
	return &outboxRepo{db: db, logger: logger}
}

func (r *outboxRepo) InsertTx(ctx context.Context, tx *sql.Tx, event models.OutboxEvent) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO event_outbox (aggregate_id, job_id, event, payload, max_attempts) VALUES ($1, $2, $3, $4, $5)`,
		event.AggregateID, event.JobID, event.Event, event.Payload, event.MaxAttempts,
	)
	return err
}

func (r *outboxRepo) FetchPendingBatch(ctx context.Context, limit int) ([]models.OutboxEvent, error) {
	var rows []models.OutboxEvent
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, aggregate_id, job_id, event, payload, status, attempts, max_attempts, last_error, created_at, published_at
		 FROM event_outbox WHERE status = 'pending' ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	return rows, err
}

func (r *outboxRepo) MarkPublished(ctx context.Context, ids []int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE event_outbox SET status = 'published', published_at = now() WHERE id = ANY($1)`,
		pq.Array(ids))
	return err
}

func (r *outboxRepo) IncrementAttempts(ctx context.Context, id int64, errMsg string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE event_outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1`,
		id, errMsg)
	return err
}

func (r *outboxRepo) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE event_outbox SET status = 'failed', last_error = $2 WHERE id = $1`,
		id, errMsg)
	return err
}

func (r *outboxRepo) DeletePublishedBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM event_outbox WHERE status = 'published' AND published_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *outboxRepo) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.GetContext(ctx, &count, `SELECT COUNT(*) FROM event_outbox WHERE status = 'pending'`)
	return count, err
}
