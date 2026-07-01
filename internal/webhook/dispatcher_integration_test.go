//go:build integration

package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/internal/webhook"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/zap"
)

const testEncryptionKey = "01234567890123456789012345678901"

func setupDB(t *testing.T, ctx context.Context) *sqlx.DB {
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

	dsn, _ := pg.ConnectionString(ctx, "sslmode=disable")
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, ddl := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE TABLE assets (
			asset_id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			original_url TEXT NOT NULL, type TEXT NOT NULL, status TEXT NOT NULL,
			mime_type TEXT NOT NULL, size_bytes BIGINT NOT NULL, owner_id TEXT,
			created_at TIMESTAMPTZ DEFAULT NOW(), updated_at TIMESTAMPTZ DEFAULT NOW())`,
		`CREATE TABLE webhook_registrations (
			id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			user_id TEXT NOT NULL DEFAULT '', url TEXT NOT NULL,
			secret TEXT NOT NULL, events JSONB NOT NULL DEFAULT '[]'::jsonb,
			created_at TIMESTAMPTZ DEFAULT now())`,
		`CREATE TABLE webhook_deliveries (
			id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
			registration_id UUID NOT NULL REFERENCES webhook_registrations(id) ON DELETE CASCADE,
			event TEXT NOT NULL, asset_id UUID NOT NULL, job_id BIGINT NOT NULL,
			payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
			attempts INT NOT NULL DEFAULT 0, next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			delivered_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("DDL: %v", err)
		}
	}
	return db
}

func TestDispatcher_DeliversSuccessfully(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t, ctx)

	secret := "test-secret-value"
	encSecret, _ := utils.GenerateToken(secret, testEncryptionKey)

	// Set up a test HTTP server that records calls.
	var received atomic.Int32
	var receivedBody []byte
	var receivedSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		receivedBody, _ = io.ReadAll(r.Body)
		receivedSig = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Insert registration + asset + delivery.
	regID := uuid.New()
	assetID := uuid.New()
	payload, _ := json.Marshal(map[string]interface{}{
		"event": "job.done", "asset_id": assetID.String(), "job_id": 1, "status": "done",
	})

	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_registrations (id, user_id, url, secret, events) VALUES ($1,$2,$3,$4,$5)`,
		regID, "user-1", srv.URL, encSecret, `["job.done"]`)
	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries (registration_id, event, asset_id, job_id, payload) VALUES ($1,$2,$3,$4,$5)`,
		regID, "job.done", assetID, 1, payload)

	// Run dispatcher.
	dispCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m, reader := metrics.NewTestMetrics()
	d := webhook.NewDispatcher(db, zap.NewNop(), webhook.DispatcherConfig{
		PollInterval:  50 * time.Millisecond,
		BatchSize:     10,
		Timeout:       5 * time.Second,
		MaxAttempts:   5,
		EncryptionKey: testEncryptionKey,
		Retention:     168 * time.Hour,
	}, m)
	go d.Start(dispCtx)

	// Wait for delivery.
	deadline := time.After(3 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for webhook delivery")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	cancel()

	// Verify signature.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expectedSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if receivedSig != expectedSig {
		t.Errorf("signature mismatch: got %s, want %s", receivedSig, expectedSig)
	}

	// Verify DB status.
	var status string
	_ = db.GetContext(ctx, &status, `SELECT status FROM webhook_deliveries WHERE asset_id = $1`, assetID)
	if status != "delivered" {
		t.Errorf("expected delivered, got %s", status)
	}

	// Verify the delivery metric was recorded with status=delivered.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	var deliveredTotal int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "webhook.delivery.total" {
				continue
			}
			if sum, ok := mt.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					if v, ok := dp.Attributes.Value("status"); ok && v.AsString() == "delivered" {
						deliveredTotal += dp.Value
					}
				}
			}
		}
	}
	if deliveredTotal < 1 {
		t.Errorf("expected >=1 delivered webhook.delivery.total metric, got %d", deliveredTotal)
	}
}

func TestDispatcher_RetriesOnFailure(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t, ctx)

	encSecret, _ := utils.GenerateToken("secret", testEncryptionKey)

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	regID := uuid.New()
	assetID := uuid.New()
	payload, _ := json.Marshal(map[string]interface{}{"event": "job.done"})

	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_registrations (id, user_id, url, secret, events) VALUES ($1,$2,$3,$4,$5)`,
		regID, "user-1", srv.URL, encSecret, `["job.done"]`)
	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries (registration_id, event, asset_id, job_id, payload) VALUES ($1,$2,$3,$4,$5)`,
		regID, "job.done", assetID, 1, payload)

	// Run dispatcher briefly — first attempt should fail and schedule retry.
	dispCtx, cancel := context.WithCancel(ctx)
	d := webhook.NewDispatcher(db, zap.NewNop(), webhook.DispatcherConfig{
		PollInterval:  50 * time.Millisecond,
		BatchSize:     10,
		Timeout:       2 * time.Second,
		MaxAttempts:   5,
		EncryptionKey: testEncryptionKey,
		Retention:     168 * time.Hour,
	}, nil)
	go d.Start(dispCtx)
	time.Sleep(300 * time.Millisecond)
	cancel()

	if callCount.Load() < 1 {
		t.Fatal("expected at least 1 call")
	}

	// Delivery should still be pending with attempts incremented.
	var row struct {
		Status   string `db:"status"`
		Attempts int    `db:"attempts"`
	}
	_ = db.GetContext(ctx, &row, `SELECT status, attempts FROM webhook_deliveries WHERE asset_id = $1`, assetID)
	if row.Status != "pending" {
		t.Errorf("expected pending, got %s", row.Status)
	}
	if row.Attempts < 1 {
		t.Errorf("expected attempts >= 1, got %d", row.Attempts)
	}
}

func TestDispatcher_FailsAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t, ctx)

	encSecret, _ := utils.GenerateToken("secret", testEncryptionKey)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	regID := uuid.New()
	assetID := uuid.New()
	payload, _ := json.Marshal(map[string]interface{}{"event": "job.done"})

	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_registrations (id, user_id, url, secret, events) VALUES ($1,$2,$3,$4,$5)`,
		regID, "user-1", srv.URL, encSecret, `["job.done"]`)
	// Pre-set attempts to 4 (max-1), so next failure marks it failed.
	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries (registration_id, event, asset_id, job_id, payload, attempts) VALUES ($1,$2,$3,$4,$5,4)`,
		regID, "job.done", assetID, 1, payload)

	dispCtx, cancel := context.WithCancel(ctx)
	d := webhook.NewDispatcher(db, zap.NewNop(), webhook.DispatcherConfig{
		PollInterval:  50 * time.Millisecond,
		BatchSize:     10,
		Timeout:       2 * time.Second,
		MaxAttempts:   5,
		EncryptionKey: testEncryptionKey,
		Retention:     168 * time.Hour,
	}, nil)
	go d.Start(dispCtx)
	time.Sleep(300 * time.Millisecond)
	cancel()

	var status string
	_ = db.GetContext(ctx, &status, `SELECT status FROM webhook_deliveries WHERE asset_id = $1`, assetID)
	if status != "failed" {
		t.Errorf("expected failed, got %s", status)
	}
}

// TestDispatcher_DeliversConcurrently verifies that a batch larger than the
// concurrency limit is delivered in parallel (max in-flight > 1, bounded by the
// limit), every delivery completes, and the delivery metric counts them all.
func TestDispatcher_DeliversConcurrently(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t, ctx)

	encSecret, _ := utils.GenerateToken("secret", testEncryptionKey)

	const total = 20
	const concurrency = 5

	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	var delivered atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond) // hold the connection so overlap is observable
		inFlight.Add(-1)
		delivered.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	regID := uuid.New()
	_, _ = db.ExecContext(ctx,
		`INSERT INTO webhook_registrations (id, user_id, url, secret, events) VALUES ($1,$2,$3,$4,$5)`,
		regID, "user-1", srv.URL, encSecret, `["job.done"]`)
	for i := 0; i < total; i++ {
		assetID := uuid.New()
		payload, _ := json.Marshal(map[string]interface{}{"event": "job.done"})
		_, _ = db.ExecContext(ctx,
			`INSERT INTO webhook_deliveries (registration_id, event, asset_id, job_id, payload) VALUES ($1,$2,$3,$4,$5)`,
			regID, "job.done", assetID, int64(i), payload)
	}

	dispCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m, reader := metrics.NewTestMetrics()
	d := webhook.NewDispatcher(db, zap.NewNop(), webhook.DispatcherConfig{
		PollInterval:  50 * time.Millisecond,
		BatchSize:     total,
		Timeout:       5 * time.Second,
		MaxAttempts:   5,
		EncryptionKey: testEncryptionKey,
		Retention:     168 * time.Hour,
		Concurrency:   concurrency,
	}, m)
	go d.Start(dispCtx)

	deadline := time.After(10 * time.Second)
	for delivered.Load() < total {
		select {
		case <-deadline:
			t.Fatalf("timeout: delivered %d/%d", delivered.Load(), total)
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	cancel()

	// Parallelism actually happened, and stayed within the bound.
	if maxInFlight.Load() < 2 {
		t.Errorf("expected concurrent delivery (max in-flight > 1), got %d", maxInFlight.Load())
	}
	if maxInFlight.Load() > concurrency {
		t.Errorf("max in-flight %d exceeded concurrency limit %d", maxInFlight.Load(), concurrency)
	}

	// All rows delivered in the DB.
	var pending int
	_ = db.GetContext(ctx, &pending, `SELECT count(*) FROM webhook_deliveries WHERE status != 'delivered'`)
	if pending != 0 {
		t.Errorf("expected 0 non-delivered rows, got %d", pending)
	}

	// Metric total counts every delivery.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	var deliveredTotal int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "webhook.delivery.total" {
				continue
			}
			if sum, ok := mt.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					if v, ok := dp.Attributes.Value("status"); ok && v.AsString() == "delivered" {
						deliveredTotal += dp.Value
					}
				}
			}
		}
	}
	if deliveredTotal != total {
		t.Errorf("expected %d delivered metric, got %d", total, deliveredTotal)
	}
}
