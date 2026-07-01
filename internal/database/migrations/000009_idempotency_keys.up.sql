-- Idempotency keys: Stripe-style full-response replay, scoped per tenant.
-- The first request for a (tenant_id, key) inserts a 'pending' row (the unique
-- PK acts as a lock); once the handler completes, the response is stored and the
-- row flips to 'done'. Replays within the TTL return the stored response.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    tenant_id           TEXT        NOT NULL,
    key                 TEXT        NOT NULL,
    request_fingerprint TEXT        NOT NULL,   -- sha256(method+path+body)
    status              TEXT        NOT NULL DEFAULT 'pending',  -- pending | done
    response_status     INT,
    response_body       JSONB,
    asset_id            UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL,

    PRIMARY KEY (tenant_id, key)
);

-- Supports the background TTL sweep.
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires ON idempotency_keys (expires_at);
