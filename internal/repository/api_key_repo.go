package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"go.uber.org/zap"
)

// ErrAPIKeyNotFound is returned when no api_keys row matches a presented hash.
var ErrAPIKeyNotFound = errors.New("api key not found")

// APIKey is a row in the api_keys table. The plaintext key is never stored —
// only KeyHash (SHA-256 hex) plus the public Prefix.
type APIKey struct {
	ID        uuid.UUID  `db:"id"`
	TenantID  string     `db:"tenant_id"`
	KeyHash   string     `db:"key_hash"`
	Prefix    string     `db:"prefix"`
	ScopesRaw []byte     `db:"scopes"`
	ExpiresAt *time.Time `db:"expires_at"`
	RevokedAt *time.Time `db:"revoked_at"`
	CreatedAt time.Time  `db:"created_at"`
}

// Scopes decodes the JSONB scopes column.
func (k *APIKey) Scopes() []string {
	if len(k.ScopesRaw) == 0 {
		return nil
	}
	var s []string
	_ = json.Unmarshal(k.ScopesRaw, &s)
	return s
}

type APIKeyRepository interface {
	// Create inserts a new API key row. scopes is persisted as JSONB.
	Create(ctx context.Context, tenantID, keyHash, prefix string, scopes []string, expiresAt *time.Time) (uuid.UUID, error)
	// GetByHash returns the key matching keyHash, or ErrAPIKeyNotFound.
	GetByHash(ctx context.Context, keyHash string) (*APIKey, error)
}

type apiKeyRepo struct {
	db     *sqlx.DB
	logger *zap.Logger
}

func NewAPIKeyRepository(db *sqlx.DB, logger *zap.Logger) APIKeyRepository {
	return &apiKeyRepo{db: db, logger: logger}
}

func (r *apiKeyRepo) Create(ctx context.Context, tenantID, keyHash, prefix string, scopes []string, expiresAt *time.Time) (uuid.UUID, error) {
	if scopes == nil {
		scopes = []string{}
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = r.db.QueryRowxContext(ctx,
		`INSERT INTO api_keys (tenant_id, key_hash, prefix, scopes, expires_at)
		 VALUES ($1, $2, $3, $4::jsonb, $5)
		 RETURNING id`,
		tenantID, keyHash, prefix, scopesJSON, expiresAt,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (r *apiKeyRepo) GetByHash(ctx context.Context, keyHash string) (*APIKey, error) {
	var k APIKey
	err := r.db.GetContext(ctx, &k,
		`SELECT id, tenant_id, key_hash, prefix, scopes, expires_at, revoked_at, created_at
		 FROM api_keys WHERE key_hash = $1`, keyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}
