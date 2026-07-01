//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func setupIdempotencyDB(t *testing.T, ctx context.Context) *sqlx.DB {
	t.Helper()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE idempotency_keys (
		tenant_id TEXT NOT NULL, key TEXT NOT NULL, request_fingerprint TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending', response_status INT, response_body JSONB,
		asset_id UUID, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), expires_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (tenant_id, key))`); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return db
}

// TestIdempotency_ConcurrentAcquire_SingleWinner is the no-race-dupes test:
// many concurrent requests with the same (tenant, key, fingerprint) must yield
// exactly one AcquireAcquired; the rest see AcquireInFlight.
func TestIdempotency_ConcurrentAcquire_SingleWinner(t *testing.T) {
	ctx := context.Background()
	db := setupIdempotencyDB(t, ctx)
	repo := repository.NewIdempotencyRepository(db, zap.NewNop())

	const n = 20
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		acquired  int
		inflight  int
		otherSeen int
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			outcome, _, err := repo.Acquire(ctx, "tenant-a", "key-1", "fp-1", time.Hour)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			mu.Lock()
			switch outcome {
			case repository.AcquireAcquired:
				acquired++
			case repository.AcquireInFlight:
				inflight++
			default:
				otherSeen++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if acquired != 1 {
		t.Errorf("acquired = %d, want exactly 1", acquired)
	}
	if inflight != n-1 {
		t.Errorf("inflight = %d, want %d", inflight, n-1)
	}
	if otherSeen != 0 {
		t.Errorf("unexpected outcomes = %d, want 0", otherSeen)
	}
}

// TestIdempotency_ReplayAndMismatch covers the done-replay and
// different-fingerprint paths.
func TestIdempotency_ReplayAndMismatch(t *testing.T) {
	ctx := context.Background()
	db := setupIdempotencyDB(t, ctx)
	repo := repository.NewIdempotencyRepository(db, zap.NewNop())

	// First acquire wins.
	if o, _, _ := repo.Acquire(ctx, "t", "k", "fp", time.Hour); o != repository.AcquireAcquired {
		t.Fatalf("first acquire = %v, want Acquired", o)
	}
	// Same key while pending -> in flight.
	if o, _, _ := repo.Acquire(ctx, "t", "k", "fp", time.Hour); o != repository.AcquireInFlight {
		t.Errorf("pending re-acquire = %v, want InFlight", o)
	}
	// Complete, then a matching replay returns the stored response.
	if err := repo.Complete(ctx, "t", "k", 201, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	o, rec, _ := repo.Acquire(ctx, "t", "k", "fp", time.Hour)
	if o != repository.AcquireReplay {
		t.Fatalf("post-complete acquire = %v, want Replay", o)
	}
	// Postgres JSONB normalizes whitespace on storage (e.g. `{"ok":true}` is
	// re-serialized as `{"ok": true}`), so compare semantic JSON rather than
	// raw bytes — the replay contract is content-equality, not byte equality.
	if rec == nil || rec.ResponseStatus != 201 {
		t.Fatalf("replay record = %+v", rec)
	}
	var got, want any
	if err := json.Unmarshal(rec.ResponseBody, &got); err != nil {
		t.Fatalf("unmarshal stored body: %v (raw=%q)", err, rec.ResponseBody)
	}
	if err := json.Unmarshal([]byte(`{"ok":true}`), &want); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replay body = %s, want {\"ok\":true}", rec.ResponseBody)
	}
	// Same key, different fingerprint -> mismatch.
	if o, _, _ := repo.Acquire(ctx, "t", "k", "different-fp", time.Hour); o != repository.AcquireMismatch {
		t.Errorf("different-fingerprint acquire = %v, want Mismatch", o)
	}
}
