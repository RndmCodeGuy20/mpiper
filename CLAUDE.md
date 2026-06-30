# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Dev server (hot-reload via air)
task run

# Dev server (go run, no hot-reload)
task dev                       # ENV=development, LOG_LEVEL=DEBUG
task staging                   # ENV=staging,     LOG_LEVEL=INFO
task prod                      # ENV=production,  LOG_LEVEL=WARN

# Run tests
task test                      # uses gotestsum
task test -- ./internal/...    # run specific package
task test-coverage             # generates coverage.html

# Lint / format
task lint                      # golangci-lint
task fmt                       # go fmt + goimports

# Build
task build                     # outputs build/mpiper.exe
task build-prod                # ENV=production

# Python worker (from project root)
poetry run python -m worker

# Python tests
poetry run pytest worker/tests/

# Docker
task docker-build && task docker-run          # API
task docker-build-worker && task docker-run-worker  # Worker
```

Env files: `development` → `.env.local`, `staging` → `.env.staging`, `production` → `.env`.

`ENV`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `REDIS_CONNECTION_STRING`, and `ENCRYPTION_KEY` (exactly 32 bytes) are required — the config will panic without them.

## Architecture

Two-service pipeline: **Go API server** + **Python media worker**, communicating via **Redis Streams** (`media:jobs` stream). Postgres is the durable source of truth; Redis is transport-only.

### Go API server (`cmd/server`, `internal/`)

Entry point: `cmd/server/main.go`

1. `config.InitializeConfig` → `config.Init` — loads env file for the current `Env` build variable, stores singleton (`config.MustGet()` available everywhere after startup).
2. `pkg/logger.New` — builds a `*zap.Logger` with optional OTel log export.
3. `metrics.InitTracer` / `metrics.InitMetrics` — wires up OTel tracing + metrics exporters.
4. `database.NewPostgresDB` — `sqlx.DB` pool; if `AUTO_MIGRATE=true` runs embedded SQL migrations on startup.
5. `server.NewServer` → `server.Start` — Chi router with middleware stack: request-ID, logger, tracing, metrics, recovery, slow-request detector, CORS, auth.

Layer layout inside `internal/`:
- `handler/` — HTTP handlers, read request → call service → write response via `pkg/utils/response`
- `service/` — business logic (`AssetService`); coordinates repo + queue + storage
- `repository/` — SQL queries via sqlx (`AssetRepository`)
- `router/` — Chi route registration; mounts handlers onto the router returned to `server/`
- `models/` — request/response structs (`UploadAssetRequest`, `UploadAssetResponse`); not DB models
- `queue/` — `RedisQueue.Enqueue` writes to the stream with OTel tracing + retry
- `metrics/` — OTel metric instruments (counters, histograms); `internal/metrics/metrics.go` defines all instruments, `otel.go` handles provider init/shutdown

### Python worker (`worker/`)

Entry: `worker/__main__.py` → `consumer/main.py`

- `Consumer` (Redis Streams, consumer group) polls with `xreadgroup`, processes one message at a time.
- Message contains either `job_id` or `asset_id`. `job_id` is canonical; `asset_id` triggers an upsert into the `jobs` table first.
- `_handle_job` takes a `SELECT … FOR UPDATE` lock, marks the row `in_progress`, calls `process_asset_dispatch`, then marks `done` + acks the stream message. On failure it re-queues (up to `MAX_JOB_ATTEMPTS`).
- `_recover_stuck_pending` re-adds `pending/in_progress` jobs older than 2 min back to the stream (recovery path, called when no messages available).
- `worker/processing/processor.py` — `process_asset_dispatch` routes by asset type to `images.py` or `videos.py`.
- `worker/storage/` — `StorageX` ABC; `GCSStorage` and `S3Storage` concrete impls (selected by a factory in `worker/storage/__init__.py`). `S3Storage` mirrors the Go split-endpoint behavior: object I/O uses `endpoint_url`, persisted variant URLs use `public_endpoint_url`.
- `worker/utils/metrics.py` — Prometheus metrics via `prometheus_client`.

### Shared concerns

**Config singleton (Go):** `internal/config.MustGet()` — call only after `config.Init(cfg)` in `main`. Do not pass `*EnvConfig` via function params; use the singleton.

**Logger (Go):** `pkg/logger` wraps zap. Request-scoped logger lives in `context`; retrieve with `applogger.FromContext(ctx)` or `middleware.LoggerFromContext(ctx)`. Base logger is constructed once in `main` and passed to subsystems.

**Error types (Go):** `pkg/errors` has typed API errors (`NotFoundError`, `BadRequestError`, `UnauthorizedError`, `ConflictError`, `InternalServerErrorError`) each embedding `*ApiError` (carries `StatusCode`). Handler layer type-asserts on these to set HTTP status. Use `fmt.Errorf("op: %w", err)` for internal wrapping; use `errors.New*` constructors (e.g. `errors.NewNotFoundError`) at the service/handler boundary.

**Storage (`pkg/utils/storagex`):** `StorageX` interface with `PutObject`, `GetObject`, `GeneratePresignedURL`, `PublicURL`, `DeleteObject`. Implementations: `GCSStorage` and `s3Storage` (S3 / S3-compatible MinIO). The S3 impl supports a split endpoint — `Endpoint` (internal/server-side) for object I/O and `PublicEndpoint` (client-facing) for presigned + public URLs; presigning happens against the public endpoint because SigV4 signs the Host header.

**OTel:** Full tracing + metrics on the API side. Go instruments are in `internal/metrics/metrics.go`. Collector config at `observability/otel-collector.yml`; Grafana/Loki/Tempo/Prometheus configs in `observability/`. Python side uses `prometheus_client` (not OTel).

### Database schema

- `assets` — core media record; `status` enum: `uploading → uploaded → processing → ready / failed`
- `variants.image` — deduplicated by `variant_hash` (content+params hash); immutable once written
- `jobs` — processing job per asset; `status` enum: `pending → in_progress → done / failed`; `attempts` tracked for retry cap

Migrations are plain SQL in `db/migrations/`. The Go server can auto-run them at startup (`AUTO_MIGRATE=true`); the Python worker also runs them via `worker/consumer/migrations.py`.
