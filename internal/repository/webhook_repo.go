package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

type WebhookRegistration struct {
	ID        uuid.UUID `db:"id" json:"id"`
	UserID    string    `db:"user_id" json:"user_id"`
	URL       string    `db:"url" json:"url"`
	Secret    string    `db:"secret" json:"-"`
	Events    []string  `db:"-" json:"events"`
	EventsRaw []byte    `db:"events" json:"-"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type WebhookRepository interface {
	Create(ctx context.Context, reg WebhookRegistration) error
	ListByUser(ctx context.Context, userID string) ([]WebhookRegistration, error)
	Delete(ctx context.Context, id uuid.UUID, userID string) error
}

type webhookRepo struct {
	db     *sqlx.DB
	logger *zap.Logger
}

func NewWebhookRepository(db *sqlx.DB, logger *zap.Logger) WebhookRepository {
	return &webhookRepo{db: db, logger: logger}
}

func (r *webhookRepo) Create(ctx context.Context, reg WebhookRegistration) error {
	eventsJSON, _ := json.Marshal(reg.Events)
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO webhook_registrations (id, user_id, url, secret, events) VALUES ($1, $2, $3, $4, $5)`,
		reg.ID, reg.UserID, reg.URL, reg.Secret, eventsJSON,
	)
	return err
}

func (r *webhookRepo) ListByUser(ctx context.Context, userID string) ([]WebhookRegistration, error) {
	var rows []WebhookRegistration
	err := r.db.SelectContext(ctx, &rows,
		`SELECT id, user_id, url, events, created_at FROM webhook_registrations WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		_ = json.Unmarshal(rows[i].EventsRaw, &rows[i].Events)
	}
	return rows, nil
}

func (r *webhookRepo) Delete(ctx context.Context, id uuid.UUID, userID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM webhook_registrations WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

var ErrNotFound = &notFoundError{}

type notFoundError struct{}

func (e *notFoundError) Error() string { return "not found" }
