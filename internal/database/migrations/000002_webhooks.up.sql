CREATE TABLE IF NOT EXISTS webhook_registrations (
    id          UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    url         TEXT        NOT NULL,
    secret      TEXT        NOT NULL,        -- encrypted at rest (ENCRYPTION_KEY)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id              UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    registration_id UUID        NOT NULL REFERENCES webhook_registrations (id) ON DELETE CASCADE,
    event           TEXT        NOT NULL,    -- 'job.done' | 'job.failed'
    asset_id        UUID        NOT NULL,
    job_id          BIGINT      NOT NULL,
    payload         JSONB       NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending',  -- pending | delivered | failed
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS webhook_deliveries_pending_idx
    ON webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';
