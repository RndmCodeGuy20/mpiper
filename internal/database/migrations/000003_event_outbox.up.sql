CREATE TABLE IF NOT EXISTS event_outbox (
    id            BIGSERIAL   PRIMARY KEY,
    aggregate_id  UUID        NOT NULL,
    job_id        BIGINT,
    event         TEXT        NOT NULL,
    payload       JSONB       NOT NULL,
    traceparent   TEXT,
    status        TEXT        NOT NULL DEFAULT 'pending',
    attempts      INT         NOT NULL DEFAULT 0,
    max_attempts  INT         NOT NULL DEFAULT 5,
    last_error    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at  TIMESTAMPTZ
);

CREATE INDEX idx_event_outbox_pending ON event_outbox (id) WHERE status = 'pending';
