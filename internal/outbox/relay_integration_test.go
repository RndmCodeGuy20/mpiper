//go:build integration

package outbox_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/rndmcodeguy20/mpiper/internal/models"
	"github.com/rndmcodeguy20/mpiper/internal/outbox"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

// --- helpers ---

func setupPostgres(t *testing.T, ctx context.Context) *sqlx.DB {
	t.Helper()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Apply schema for event_outbox (and assets/jobs for the full flow test).
	for _, ddl := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE TABLE IF NOT EXISTS assets (
			asset_id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			original_url TEXT NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL,
			mime_type TEXT NOT NULL, size_bytes BIGINT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE TABLE IF NOT EXISTS jobs (
			job_id BIGSERIAL PRIMARY KEY, asset_id UUID NOT NULL REFERENCES assets(asset_id),
			type TEXT NOT NULL, status TEXT NOT NULL, attempts INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE UNIQUE INDEX IF NOT EXISTS jobs_asset_type_unique ON jobs (asset_id, type)`,
		`CREATE TABLE IF NOT EXISTS event_outbox (
			id BIGSERIAL PRIMARY KEY, aggregate_id UUID NOT NULL, job_id BIGINT,
			event TEXT NOT NULL, payload JSONB NOT NULL, traceparent TEXT,
			status TEXT NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0,
			max_attempts INT NOT NULL DEFAULT 5, last_error TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(), published_at TIMESTAMPTZ)`,
		`CREATE INDEX idx_event_outbox_pending ON event_outbox (id) WHERE status = 'pending'`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("apply DDL: %v", err)
		}
	}
	return db
}

func setupRedis(t *testing.T, ctx context.Context) *redis.Client {
	t.Helper()
	rc, err := tcredis.Run(ctx, "redis:7-alpine",
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp").WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() { _ = rc.Terminate(ctx) })

	ep, err := rc.Endpoint(ctx, "")
	if err != nil {
		t.Fatalf("get redis endpoint: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{Addr: ep})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

// testQueue implements queue.Queue for integration tests.
type testQueue struct {
	rdb    *redis.Client
	stream string
}

func (q *testQueue) Enqueue(ctx context.Context, payload map[string]interface{}) (string, error) {
	body, _ := json.Marshal(payload)
	return q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: q.stream,
		Values: map[string]interface{}{"body": string(body)},
		ID:     "*",
	}).Result()
}

// failQueue always returns an error on Enqueue.
type failQueue struct{}

func (q *failQueue) Enqueue(_ context.Context, _ map[string]interface{}) (string, error) {
	return "", fmt.Errorf("simulated redis failure")
}

// --- tests ---

func TestOutboxRelay_HappyPath(t *testing.T) {
	ctx := context.Background()
	db := setupPostgres(t, ctx)
	rdb := setupRedis(t, ctx)
	logger := zap.NewNop()
	stream := "media:jobs"

	repo := repository.NewOutboxRepository(db, logger)
	q := &testQueue{rdb: rdb, stream: stream}

	// Insert an asset and simulate the MarkAssetUploaded transaction.
	assetID := uuid.New()
	_, err := db.ExecContext(ctx,
		`INSERT INTO assets (asset_id, original_url, type, mime_type, status, size_bytes) VALUES ($1,$2,$3,$4,$5,$6)`,
		assetID, "http://example.com/raw", "image", "image/jpeg", "uploaded", 1024)
	if err != nil {
		t.Fatalf("insert asset: %v", err)
	}

	var jobID int64
	err = db.QueryRowContext(ctx,
		`INSERT INTO jobs (asset_id, type, status) VALUES ($1, 'process_asset', 'processing') RETURNING job_id`, assetID).Scan(&jobID)
	if err != nil {
		t.Fatalf("insert job: %v", err)
	}

	// Insert outbox row (what the producer does inside the transaction).
	payload, _ := json.Marshal(map[string]interface{}{
		"job_id":   jobID,
		"asset_id": assetID.String(),
		"event":    "asset_uploaded",
	})
	tx, _ := db.BeginTx(ctx, nil)
	err = repo.InsertTx(ctx, tx, models.OutboxEvent{
		AggregateID: assetID,
		JobID:       &jobID,
		Event:       "asset_uploaded",
		Payload:     payload,
	})
	if err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}
	_ = tx.Commit()

	// Assert: outbox row pending, no Redis message yet.
	var status string
	_ = db.GetContext(ctx, &status, `SELECT status FROM event_outbox WHERE aggregate_id = $1`, assetID)
	if status != "pending" {
		t.Fatalf("expected pending, got %s", status)
	}
	msgs, _ := rdb.XRange(ctx, stream, "-", "+").Result()
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}

	// Start the relay with a short interval.
	relayCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	relay := outbox.NewRelay(repo, q, logger, nil, 50*time.Millisecond, 100)
	go relay.Start(relayCtx)

	// Wait for message to appear on the stream.
	deadline := time.After(2 * time.Second)
	for {
		msgs, _ = rdb.XRange(ctx, stream, "-", "+").Result()
		if len(msgs) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for message on Redis stream")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Verify message content.
	var body map[string]interface{}
	_ = json.Unmarshal([]byte(msgs[0].Values["body"].(string)), &body)
	if body["asset_id"] != assetID.String() {
		t.Fatalf("expected asset_id %s, got %v", assetID, body["asset_id"])
	}

	// Assert outbox row is now published.
	_ = db.GetContext(ctx, &status, `SELECT status FROM event_outbox WHERE aggregate_id = $1`, assetID)
	if status != "published" {
		t.Fatalf("expected published, got %s", status)
	}
}

func TestOutboxRelay_FailureMarksRowFailed(t *testing.T) {
	ctx := context.Background()
	db := setupPostgres(t, ctx)
	logger := zap.NewNop()

	repo := repository.NewOutboxRepository(db, logger)

	// Insert an outbox row with max_attempts=1 so first failure → failed.
	assetID := uuid.New()
	payload, _ := json.Marshal(map[string]interface{}{"event": "test"})
	tx, _ := db.BeginTx(ctx, nil)
	_, err := tx.ExecContext(ctx,
		`INSERT INTO event_outbox (aggregate_id, event, payload, max_attempts) VALUES ($1, $2, $3, 1)`,
		assetID, "asset_uploaded", payload)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = tx.Commit()

	// Run one relay tick with a failing queue.
	relay := outbox.NewRelay(repo, &failQueue{}, logger, nil, 50*time.Millisecond, 100)
	tickCtx, tickCancel := context.WithCancel(ctx)
	go relay.Start(tickCtx)
	time.Sleep(200 * time.Millisecond)
	tickCancel()

	// Verify the row is marked failed.
	var row struct {
		Status    string  `db:"status"`
		LastError *string `db:"last_error"`
	}
	err = db.GetContext(ctx, &row, `SELECT status, last_error FROM event_outbox WHERE aggregate_id = $1`, assetID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if row.Status != "failed" {
		t.Fatalf("expected failed, got %s", row.Status)
	}
	if row.LastError == nil || *row.LastError == "" {
		t.Fatal("expected last_error to be set")
	}
}

func TestOutboxRelay_Cleanup(t *testing.T) {
	ctx := context.Background()
	db := setupPostgres(t, ctx)
	logger := zap.NewNop()

	repo := repository.NewOutboxRepository(db, logger)

	// Insert a published row with old published_at.
	assetID := uuid.New()
	payload, _ := json.Marshal(map[string]interface{}{"event": "test"})
	oldTime := time.Now().Add(-200 * time.Hour)
	_, err := db.ExecContext(ctx,
		`INSERT INTO event_outbox (aggregate_id, event, payload, status, published_at) VALUES ($1, $2, $3, 'published', $4)`,
		assetID, "asset_uploaded", payload, oldTime)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Verify row exists.
	var count int
	_ = db.GetContext(ctx, &count, `SELECT COUNT(*) FROM event_outbox WHERE aggregate_id = $1`, assetID)
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}

	// Run cleanup with 168h retention.
	deleted, err := repo.DeletePublishedBefore(ctx, time.Now().Add(-168*time.Hour))
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	// Verify row is gone.
	_ = db.GetContext(ctx, &count, `SELECT COUNT(*) FROM event_outbox WHERE aggregate_id = $1`, assetID)
	if count != 0 {
		t.Fatalf("expected 0 rows, got %d", count)
	}
}

// Ensure InsertTx uses the provided transaction (rolls back correctly).
func TestOutboxRepo_InsertTx_RollbackDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	db := setupPostgres(t, ctx)
	logger := zap.NewNop()

	repo := repository.NewOutboxRepository(db, logger)

	assetID := uuid.New()
	payload, _ := json.Marshal(map[string]interface{}{"event": "test"})
	tx, _ := db.BeginTx(ctx, nil)
	_ = repo.InsertTx(ctx, tx, models.OutboxEvent{
		AggregateID: assetID,
		Event:       "asset_uploaded",
		Payload:     payload,
	})
	_ = tx.Rollback()

	// Should not exist after rollback.
	var count int
	_ = db.GetContext(ctx, &count, `SELECT COUNT(*) FROM event_outbox WHERE aggregate_id = $1`, assetID)
	if count != 0 {
		t.Fatalf("expected 0 rows after rollback, got %d", count)
	}
}

// Suppress unused import warning for database/sql.
var _ = (*sql.DB)(nil)
