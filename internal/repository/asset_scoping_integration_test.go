//go:build integration

package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/rndmcodeguy20/mpiper/internal/repository"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

func setupAssetsDB(t *testing.T, ctx context.Context) *sqlx.DB {
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
		t.Fatalf("connection string: %v", err)
	}
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
			mime_type TEXT NOT NULL, size_bytes BIGINT NOT NULL, owner_id TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

func statusOf(t *testing.T, db *sqlx.DB, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := db.Get(&s, `SELECT status FROM assets WHERE asset_id = $1`, id); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func mark(t *testing.T, db *sqlx.DB, repo repository.AssetRepository, id uuid.UUID, tenant string) repository.MarkResult {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	res, err := repo.MarkAssetUploadedTx(ctx, tx, id, tenant)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("MarkAssetUploadedTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return res
}

// TestMarkAssetUploadedTx_TenantScoping is the IDOR regression test: a tenant
// must not be able to complete another tenant's asset by id.
func TestMarkAssetUploadedTx_TenantScoping(t *testing.T) {
	ctx := context.Background()
	db := setupAssetsDB(t, ctx)
	repo := repository.NewAssetRepository(db, zap.NewNop(), nil)

	id := uuid.New()
	if _, err := db.Exec(
		`INSERT INTO assets (asset_id, original_url, type, status, mime_type, size_bytes, owner_id)
		 VALUES ($1, 'http://x/raw', 'image', 'uploading', 'image/jpeg', 1, 'tenant-a')`, id,
	); err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	// tenant-b must NOT be able to complete tenant-a's asset.
	if got := mark(t, db, repo, id, "tenant-b"); got != repository.MarkNotFound {
		t.Errorf("cross-tenant mark = %v, want MarkNotFound", got)
	}
	if s := statusOf(t, db, id); s != "uploading" {
		t.Errorf("status after cross-tenant attempt = %q, want still 'uploading'", s)
	}

	// tenant-a completes its own asset.
	if got := mark(t, db, repo, id, "tenant-a"); got != repository.MarkUpdated {
		t.Errorf("owner mark = %v, want MarkUpdated", got)
	}
	if s := statusOf(t, db, id); s != "uploaded" {
		t.Errorf("status after owner mark = %q, want 'uploaded'", s)
	}

	// Re-marking by the owner is an idempotent no-op (already uploaded).
	if got := mark(t, db, repo, id, "tenant-a"); got != repository.MarkAlreadyUploaded {
		t.Errorf("repeat owner mark = %v, want MarkAlreadyUploaded", got)
	}

	// A completely unknown id is not found (for any tenant).
	if got := mark(t, db, repo, uuid.New(), "tenant-a"); got != repository.MarkNotFound {
		t.Errorf("unknown id mark = %v, want MarkNotFound", got)
	}
}
