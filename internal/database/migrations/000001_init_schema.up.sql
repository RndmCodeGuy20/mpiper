CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE IF NOT EXISTS assets (
    asset_id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    original_url       TEXT        NOT NULL,
    type               TEXT        NOT NULL,
    status             TEXT        NOT NULL,
    mime_type          TEXT        NOT NULL,
    size_bytes         BIGINT      NOT NULL,
    content_hash       TEXT,
    canonical_asset_id UUID        REFERENCES assets (asset_id) ON DELETE SET NULL,
    error_reason       TEXT,
    processed_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE SCHEMA IF NOT EXISTS variants;

CREATE TABLE IF NOT EXISTS variants.image (
    asset_id   UUID        NOT NULL REFERENCES assets (asset_id) ON DELETE CASCADE,
    url        TEXT        NOT NULL,
    role       TEXT        NOT NULL,
    width      INT,
    height     INT,
    size_bytes BIGINT,
    format     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (asset_id, role)
);

CREATE TABLE IF NOT EXISTS variants.video (
    asset_id         UUID        NOT NULL REFERENCES assets (asset_id) ON DELETE CASCADE,
    url              TEXT        NOT NULL,
    role             TEXT        NOT NULL,
    codec            TEXT,
    container        TEXT,
    resolution       TEXT,
    bitrate_kbps     INT,
    size_bytes       BIGINT,
    manifest_url     TEXT,
    duration_seconds INT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (asset_id, role)
);

CREATE TABLE IF NOT EXISTS jobs (
    job_id     BIGSERIAL   PRIMARY KEY,
    asset_id   UUID        NOT NULL REFERENCES assets (asset_id) ON DELETE CASCADE,
    type       TEXT        NOT NULL,
    status     TEXT        NOT NULL,
    attempts   INT         NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS jobs_asset_type_unique ON jobs (asset_id, type);
