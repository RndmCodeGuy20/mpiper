-- API keys: the identity source of record for tenants.
-- A key is presented as mp_<prefix>_<secret>; only its SHA-256 hash is stored.
-- tenant_id maps 1:1 to assets.owner_id / webhook_registrations.user_id.
CREATE TABLE IF NOT EXISTS api_keys (
    id         UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id  TEXT        NOT NULL,
    key_hash   TEXT        NOT NULL UNIQUE,
    prefix     TEXT        NOT NULL,
    scopes     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_prefix ON api_keys (prefix);
CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys (key_hash);
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys (tenant_id);
