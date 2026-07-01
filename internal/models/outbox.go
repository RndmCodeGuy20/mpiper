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
	// Traceparent carries the W3C trace context captured when the row was
	// written, so the distributed trace survives the outbox store-and-forward
	// hop. The relay re-activates it before publishing to Redis. Nullable:
	// rows written before this column existed (or without an active span) have
	// no trace context.
	Traceparent *string         `db:"traceparent"`
	Status      string          `db:"status"`
	Attempts    int             `db:"attempts"`
	MaxAttempts int             `db:"max_attempts"`
	LastError   *string         `db:"last_error"`
	CreatedAt   time.Time       `db:"created_at"`
	PublishedAt *time.Time      `db:"published_at"`
}
