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
    variant_id uuid PRIMARY KEY     DEFAULT uuid_generate_v4(),
    asset_id   uuid REFERENCES assets (asset_id) ON DELETE CASCADE,
    url        TEXT        NOT NULL,
    role       TEXT        NOT NULL, -- e.g., 'thumbnail', 'preview', 'full'
    format     TEXT        NOT NULL, -- e.g., 'jpeg', 'png', 'webp'
    width      INT         NOT NULL,
    height     INT         NOT NULL,
    size_bytes BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX variants_image_asset_role_unique
    ON variants.image (asset_id, role);

CREATE TABLE variants.video
(
    variant_id       uuid PRIMARY KEY     DEFAULT uuid_generate_v4(),
    asset_id         uuid REFERENCES assets (asset_id) ON DELETE CASCADE,
    url              TEXT        NOT NULL,
    role             TEXT        NOT NULL, -- e.g., 'poster', 'transcoded'
    codec            TEXT        NOT NULL, -- e.g., 'h264', 'av1'
    container        TEXT        NOT NULL, -- e.g., 'mp4', 'webm'
    resolution       TEXT        NOT NULL, -- e.g., '1080p', '720p'
    bitrate_kbps     INT,
    size_bytes       BIGINT,
    manifest_url     TEXT,                 -- for adaptive streaming formats
    duration_seconds INT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX variants_video_asset_role_unique
    ON variants.video (asset_id, role);


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
