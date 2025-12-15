-- Create the database
CREATE DATABASE mpiper_db
    WITH
    OWNER = postgres
    ENCODING = 'UTF8'
    LC_COLLATE = 'en_US.utf8'
    LC_CTYPE = 'en_US.utf8'
    TEMPLATE = template0;

-- Connect to the database
\c mpiper_db;

-- Create users
DO
$$
    BEGIN
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'admin_mpiper') THEN
            CREATE ROLE admin_mpiper LOGIN PASSWORD 'admin@mpiper';
        END IF;

        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'mpiper') THEN
            CREATE ROLE mpiper LOGIN PASSWORD 'server@mpiper';
        END IF;
    END
$$;

-- Grant privileges
GRANT ALL PRIVILEGES ON DATABASE mpiper_db TO admin_mpiper;
GRANT CONNECT ON DATABASE mpiper_db TO mpiper;

-- Switch to mpiper_db
\c mpiper_db;

-- Ensure public schema access
GRANT ALL ON SCHEMA public TO admin_mpiper;
GRANT USAGE ON SCHEMA public TO mpiper;

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TYPE asset_type AS ENUM ('image', 'video');
CREATE TYPE asset_status AS ENUM ('uploading', 'uploaded', 'processing', 'ready', 'failed');
-- CREATE TYPE variant_role AS ENUM ('thumbnail', 'preview', 'poster', 'full', 'transcoded');

-- Create assets table and variants schema for storing image and video variants
CREATE TABLE assets
(
    asset_id         uuid PRIMARY KEY      DEFAULT uuid_generate_v4(),
    original_url     TEXT         NOT NULL,
    type             asset_type   NOT NULL,
    status           asset_status NOT NULL,
    mime_type        TEXT         NOT NULL,
    size_bytes       BIGINT       NOT NULL,
    width            INT,
    height           INT,
    duration_seconds INT,
    error_reason     TEXT,
    content_hash     TEXT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE SCHEMA IF NOT EXISTS variants;
-- Grant privileges on variants schema
GRANT ALL ON SCHEMA variants TO admin_mpiper;
GRANT USAGE ON SCHEMA variants TO mpiper;
-- Create variants table
CREATE TABLE variants.image
(
    variant_hash TEXT PRIMARY KEY,     -- deterministic hash of content + params
    content_hash TEXT        NOT NULL, -- hash of raw media
    role         TEXT        NOT NULL, -- 'thumbnail', 'preview', etc
    format       TEXT        NOT NULL, -- 'jpeg', 'png', 'webp'
    width        INT         NOT NULL,
    height       INT         NOT NULL,
    size_bytes   BIGINT      NOT NULL,
    url          TEXT        NOT NULL, -- immutable storage URL
    params       JSONB       NOT NULL, -- full transformation params
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Useful lookup indexes
CREATE INDEX variants_image_content_hash_idx
    ON variants.image (content_hash);

CREATE INDEX variants_image_role_idx
    ON variants.image (role);

CREATE TABLE asset_image_variants
(
    asset_id     UUID        NOT NULL REFERENCES assets (asset_id) ON DELETE CASCADE,
    role         TEXT        NOT NULL,
    variant_hash TEXT        NOT NULL REFERENCES variants.image (variant_hash),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (asset_id, role)
);

CREATE INDEX asset_image_variants_variant_hash_idx
    ON asset_image_variants (variant_hash);


CREATE TABLE variants.video
(
    variant_hash     TEXT PRIMARY KEY,     -- deterministic hash
    content_hash     TEXT        NOT NULL,
    role             TEXT        NOT NULL, -- 'poster', 'transcoded', 'preview'
    codec            TEXT        NOT NULL, -- 'h264', 'av1'
    container        TEXT        NOT NULL, -- 'mp4', 'webm'
    resolution       TEXT        NOT NULL, -- '1280x720'
    bitrate_kbps     INT,
    size_bytes       BIGINT      NOT NULL,
    url              TEXT        NOT NULL, -- immutable
    manifest_url     TEXT,                 -- for HLS/DASH
    duration_seconds INT,
    params           JSONB       NOT NULL, -- ffmpeg + pipeline params
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX variants_video_content_hash_idx
    ON variants.video (content_hash);

CREATE INDEX variants_video_role_idx
    ON variants.video (role);

CREATE TABLE asset_video_variants
(
    asset_id     UUID        NOT NULL REFERENCES assets (asset_id) ON DELETE CASCADE,
    role         TEXT        NOT NULL,
    variant_hash TEXT        NOT NULL REFERENCES variants.video (variant_hash),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (asset_id, role)
);

CREATE INDEX asset_video_variants_variant_hash_idx
    ON asset_video_variants (variant_hash);



CREATE TABLE jobs
(
    job_id     BIGSERIAL PRIMARY KEY,
    asset_id   UUID        NOT NULL REFERENCES assets (asset_id) ON DELETE CASCADE,
    type       TEXT        NOT NULL, -- e.g., 'image_variant', 'video_variant', 'transcode'
    status     TEXT        NOT NULL, -- 'pending','in_progress','done','failed'
    attempts   INT         NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX jobs_asset_type_unique
    ON jobs (asset_id, type);


-- Grant privileges on tables to users
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA variants TO admin_mpiper;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA variants TO mpiper;

-- Grant usage on sequences to mpiper
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO mpiper;

-- Grant table-level privileges
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO admin_mpiper;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO mpiper;
