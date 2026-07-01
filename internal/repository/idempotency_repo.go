package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// AcquireOutcome is the result of trying to claim an idempotency key.
type AcquireOutcome int

const (
	// AcquireAcquired: the caller won the lock and must execute the handler.
	AcquireAcquired AcquireOutcome = iota
	// AcquireReplay: a completed response exists for this key — replay it.
	AcquireReplay
	// AcquireMismatch: the key was used before with a different request body.
	AcquireMismatch
	// AcquireInFlight: a concurrent request holds the key but hasn't finished.
	AcquireInFlight
)

// IdempotencyRecord carries a stored response for replay.
type IdempotencyRecord struct {
	ResponseStatus int
	ResponseBody   []byte
}

type IdempotencyRepository interface {
	// Acquire attempts to claim (tenant, key) for fingerprint. It is
	// concurrency-safe: exactly one concurrent caller receives AcquireAcquired.
	Acquire(ctx context.Context, tenant, key, fingerprint string, ttl time.Duration) (AcquireOutcome, *IdempotencyRecord, error)
	// Complete stores the final response and marks the key done.
	Complete(ctx context.Context, tenant, key string, status int, body []byte) error
	// Release deletes a still-pending key so the client can retry (used when
	// the handler produced a non-cacheable error, e.g. 5xx).
	Release(ctx context.Context, tenant, key string) error
	// DeleteExpired purges keys past their TTL. Returns rows deleted.
	DeleteExpired(ctx context.Context) (int64, error)
}

type idempotencyRepo struct {
	db     *sqlx.DB
	logger *zap.Logger
}

func NewIdempotencyRepository(db *sqlx.DB, logger *zap.Logger) IdempotencyRepository {
	return &idempotencyRepo{db: db, logger: logger}
}

func (r *idempotencyRepo) Acquire(ctx context.Context, tenant, key, fingerprint string, ttl time.Duration) (AcquireOutcome, *IdempotencyRecord, error) {
	expiresAt := time.Now().Add(ttl)

	// 1. Fresh claim. ON CONFLICT DO NOTHING is atomic: only one concurrent
	//    INSERT for the same (tenant, key) affects a row.
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO idempotency_keys (tenant_id, key, request_fingerprint, status, expires_at)
		 VALUES ($1, $2, $3, 'pending', $4)
		 ON CONFLICT (tenant_id, key) DO NOTHING`,
		tenant, key, fingerprint, expiresAt,
	)
	if err != nil {
		return AcquireInFlight, nil, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return AcquireAcquired, nil, nil
	}

	// 2. A row already exists. If it is expired, atomically take it over
	//    (resetting it to pending). Only one concurrent UPDATE matches, since
	//    after it runs expires_at is in the future.
	res, err = r.db.ExecContext(ctx,
		`UPDATE idempotency_keys
		 SET request_fingerprint = $3, status = 'pending',
		     response_status = NULL, response_body = NULL, asset_id = NULL,
		     created_at = NOW(), expires_at = $4
		 WHERE tenant_id = $1 AND key = $2 AND expires_at <= NOW()`,
		tenant, key, fingerprint, expiresAt,
	)
	if err != nil {
		return AcquireInFlight, nil, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return AcquireAcquired, nil, nil
	}

	// 3. A live row exists — decide replay / mismatch / in-flight.
	var (
		storedFingerprint string
		status            string
		respStatus        sql.NullInt64
		respBody          []byte
	)
	err = r.db.QueryRowxContext(ctx,
		`SELECT request_fingerprint, status, response_status, response_body
		 FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`,
		tenant, key,
	).Scan(&storedFingerprint, &status, &respStatus, &respBody)
	if errors.Is(err, sql.ErrNoRows) {
		// Raced with an expiry/sweep; treat as in-flight so the client retries.
		return AcquireInFlight, nil, nil
	}
	if err != nil {
		return AcquireInFlight, nil, err
	}

	if storedFingerprint != fingerprint {
		return AcquireMismatch, nil, nil
	}
	if status == "done" {
		return AcquireReplay, &IdempotencyRecord{
			ResponseStatus: int(respStatus.Int64),
			ResponseBody:   respBody,
		}, nil
	}
	return AcquireInFlight, nil, nil
}

func (r *idempotencyRepo) Complete(ctx context.Context, tenant, key string, status int, body []byte) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE idempotency_keys
		 SET status = 'done', response_status = $3, response_body = $4::jsonb
		 WHERE tenant_id = $1 AND key = $2`,
		tenant, key, status, body,
	)
	return err
}

func (r *idempotencyRepo) Release(ctx context.Context, tenant, key string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE tenant_id = $1 AND key = $2 AND status = 'pending'`,
		tenant, key,
	)
	return err
}

func (r *idempotencyRepo) DeleteExpired(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
