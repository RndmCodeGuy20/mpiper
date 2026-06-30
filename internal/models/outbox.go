package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type OutboxEvent struct {
	ID          int64           `db:"id"`
	AggregateID uuid.UUID       `db:"aggregate_id"`
	JobID       *int64          `db:"job_id"`
	Event       string          `db:"event"`
	Payload     json.RawMessage `db:"payload"`
	Status      string          `db:"status"`
	Attempts    int             `db:"attempts"`
	MaxAttempts int             `db:"max_attempts"`
	LastError   *string         `db:"last_error"`
	CreatedAt   time.Time       `db:"created_at"`
	PublishedAt *time.Time      `db:"published_at"`
}
