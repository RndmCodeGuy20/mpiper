# MPiper 🎬

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Release](https://img.shields.io/badge/release-v1.0.0--lts-brightgreen.svg)](https://github.com/rndmcodeguy20/mpiper/releases/tag/v1.0.0)
[![Go Version](https://img.shields.io/badge/Go-1.24-blue.svg)](https://golang.org/)
[![Python Version](https://img.shields.io/badge/Python-3.10+-blue.svg)](https://www.python.org/)

A lightweight, scalable media processing pipeline built with Go and Python. MPiper provides a robust API for uploading media assets and a distributed worker system for processing images and videos with automatic variant generation.

## 🌟 Features

- **RESTful API Server** - High-performance Go server built with Chi router
- **Concurrent Processing** - Redis Streams job queue with a **bounded worker pool** (`MAX_CONCURRENT_JOBS`) for parallel media processing — ~2.4× throughput vs single-threaded in load tests
- **Resilient delivery** - **`XAUTOCLAIM`** consumer-group recovery reclaims messages from dead workers, and poison/over-retried messages are routed to a **dead-letter stream** (`media:jobs:dlq`) instead of being dropped
- **Pluggable Storage** - GCS and S3/MinIO (any S3-compatible store) behind a single provider abstraction, selected by config
- **Image Processing** - Automatic generation of optimized, content-addressed image variants (resize, re-encode, format conversion)
- **Video Processing** - Poster generation, 720p transcode, and preview clips
- **Database-Backed** - PostgreSQL as the durable source of truth for assets, variants, and jobs
- **Webhooks** - Registration + **concurrent** signed delivery (`WEBHOOK_CONCURRENCY`) with HMAC signatures, exponential-backoff retries, and delivery tracking
- **Observability** - OpenTelemetry tracing + metrics on the API, Prometheus metrics on the worker, with a bundled Grafana/Tempo/Loki/Prometheus stack and a host-run k6 load harness
- **Docker & Kubernetes Ready** - Multi-stage images and manifests for containerized deployment

## 🏗️ Architecture

Two-service pipeline communicating over **Redis Streams** (`media:jobs`). PostgreSQL is the durable source of truth; Redis is transport-only.

```
┌─────────────┐         ┌──────────────┐         ┌─────────────┐
│   Client    │────────▶│  Go API      │────────▶│   Redis     │
│             │         │  Server      │         │   Streams   │
└─────────────┘         └──────────────┘         └─────────────┘
                               │                         │
                               ▼                         ▼
                        ┌──────────────┐         ┌─────────────┐
                        │  PostgreSQL  │◀────────│   Python    │
                        │   Database   │         │   Worker    │
                        └──────────────┘         └─────────────┘
                               │                         │
                               ▼                         ▼
                        ┌──────────────────────────────────┐
                        │   Object Storage (GCS / S3 / MinIO)│
                        └──────────────────────────────────┘
```

**Flow:**
1. Client requests an upload via the REST API
2. Go server creates the asset + job and returns a presigned upload URL
3. Client uploads the raw file directly to object storage
4. Client marks the asset uploaded; the job is enqueued on the Redis stream
5. The Python worker consumes jobs **concurrently** (a bounded pool of `MAX_CONCURRENT_JOBS`), processing media (resize, transcode, optimize)
6. Variants are written back to object storage (deduplicated by content hash)
7. Database is updated with asset status and variant metadata

**Resilience:** the worker uses Redis Streams consumer-group semantics — each
message is acked only after its job succeeds, dead-consumer messages are reclaimed
with `XAUTOCLAIM`, and poison/over-retried messages are moved to a dead-letter
stream (`media:jobs:dlq`) for inspection/replay rather than being dropped.

## 📋 Prerequisites

- **Go** 1.24 or higher
- **Python** 3.10 or higher
- **PostgreSQL** 12 or higher
- **Redis** 6 or higher
- **Task** (optional, for build automation) - [Installation guide](https://taskfile.dev/installation/)
- Object storage: a GCS bucket, or any S3-compatible store (AWS S3 / **MinIO** for fully-local runs)

## 🚀 Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/rndmcodeguy20/mpiper.git
cd mpiper
```

### 2. Configure Environment

Create a `.env.local` file in the project root (`development` → `.env.local`, `staging` → `.env.staging`, `production` → `.env`).

`ENV`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `REDIS_CONNECTION_STRING`, and `ENCRYPTION_KEY` (**exactly 32 bytes**) are required — the config panics without them.

```env
# Server
ENV=development
HOST=0.0.0.0
PORT=5010
LOG_LEVEL=DEBUG

# Database
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=your_password
DB_NAME=mpiper
DB_SSL_MODE=false
AUTO_MIGRATE=true            # run embedded SQL migrations on startup
MIGRATION_ALLOW_DESTRUCTIVE=true   # required on first bootstrap — see warning below

# Redis (transport for the job stream)
REDIS_CONNECTION_STRING=redis://localhost:6379/0

# Security (must be exactly 32 bytes)
ENCRYPTION_KEY=change_me_to_a_32_byte_secret____
# Separate 32-byte key for webhook secrets (falls back to ENCRYPTION_KEY if unset)
WEBHOOK_ENCRYPTION_KEY=change_me_to_a_diff_32_byte_secret

# Storage — pick a provider
BUCKET_PROVIDER=gcs          # gcs | s3
BUCKET_NAME=your-bucket-name

# GCS provider
GCS_SA_PATH=.secrets/service-account.json

# S3 / MinIO provider (used when BUCKET_PROVIDER=s3)
S3_BUCKET_NAME=your-bucket-name
S3_REGION=us-east-1
S3_ACCESS_KEY_ID=your-access-key
S3_SECRET_ACCESS_KEY=your-secret-key
S3_ENDPOINT_URL=http://localhost:9000          # internal/server-side endpoint (MinIO / S3-compatible)
# Optional client-facing endpoint baked into presigned + public URLs. Set this
# when internal services reach the store by a private host (e.g. http://minio:9000)
# that external clients cannot resolve. Falls back to S3_ENDPOINT_URL when empty.
S3_PUBLIC_ENDPOINT_URL=http://localhost:9000

# Worker
STREAM_NAME=media:jobs
JOB_POLL_INTERVAL=1
MAX_CONCURRENT_JOBS=5         # bounded worker-pool size; set ≈ CPU cores per worker
RECOVERY_MIN_IDLE_MS=120000  # idle threshold before XAUTOCLAIM reclaims a stuck message
STREAM_DLQ_NAME=media:jobs:dlq
SHUTDOWN_DRAIN_TIMEOUT=30     # seconds to drain in-flight jobs on SIGTERM

# Webhooks
WEBHOOK_CONCURRENCY=10        # concurrent signed deliveries per dispatcher tick
WEBHOOK_BATCH_SIZE=50
WEBHOOK_POLL_INTERVAL=2s
WEBHOOK_MAX_ATTEMPTS=5
```

> **Tuning `MAX_CONCURRENT_JOBS`:** media work is partly CPU-bound (Pillow/ffmpeg),
> so set it close to the worker's CPU-core count. Going much higher *oversubscribes*
> the cores and reduces throughput — load tests showed `mcj=8` on 4 cores was slower
> than `mcj=4`. Size worker memory to the pool, not the single-threaded baseline.

> The worker reads the same `S3_*` variables as the Go server (falling back to `BUCKET_*`), so one `.env` drives both services.

### 3. Set Up the Database

Migrations run automatically on startup when `AUTO_MIGRATE=true` — both the Go server and the Python worker apply the embedded SQL migrations.

> **Destructive migrations are gated.** Versions `000007_split_webhook_key` and
> `000008_assets_owner_not_null` drop or alter existing user data
> (`webhook_registrations`, `assets.owner_id`). Both runners refuse to apply
> them unless `MIGRATION_ALLOW_DESTRUCTIVE=true` is set. Set it for local
> bootstrap on a fresh database, but **never** set it on a database that
> already contains production data — apply those migrations by hand and review
> the SQL first.

To apply them manually instead:

```bash
createdb mpiper
psql -d mpiper -f db/migrations/001_seed.sql
```

### 4. Install Dependencies

**Go Server:**
```bash
go mod download
```

**Python Worker** (managed with Poetry):
```bash
pipx install poetry      # or: pip install poetry
poetry install
```

### 5. Run the Services

**Option A: Using Task (Recommended)**

```bash
task dev                       # API server (ENV=development, hot-reload via `task run`)

poetry run python -m worker    # worker, in another terminal
```

**Option B: Manual**

```bash
go run cmd/server/main.go      # API server
python -m worker               # worker
```

### 6. Test the API

All `/api/v1` routes require a Bearer **API key** — a scoped, revocable key
(`mp_<prefix>_<secret>`) stored SHA-256-hashed at rest (see
[`pkg/utils/apikey.go`](pkg/utils/apikey.go)). Mint one for a tenant with the
CLI (it prints the key **once**):

```bash
TOKEN="$(go run ./cmd/mint-api-key --tenant demo-user)"
# optional: --expires 720h  --scopes assets:write,webhooks:write
```

> The CLI connects to the database using your environment config (`.env.local`
> in development). For the fully containerized demo, the bundled scripts seed a
> key directly into the running Postgres — see **Run the demo** below.

Request a presigned upload URL:

```bash
curl -X POST http://localhost:5010/api/v1/storage/presign \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "fileName": "image.jpg",
    "contentType": "image/jpeg",
    "size": 1024000
  }'
```

Upload the file to the returned `uploadUrl`, then mark the asset complete to
enqueue processing:

```bash
curl -X PUT "<uploadUrl>" -H "Content-Type: image/jpeg" --data-binary @image.jpg

curl "http://localhost:5010/api/v1/assets/<assetId>/complete" \
  -H "Authorization: Bearer $TOKEN"
```

> Prefer the scripted path? [`scripts/demo-e2e.sh`](scripts/demo-e2e.sh) runs this
> entire flow (image + video + webhooks) end-to-end — see **Run the demo** below.

## 🐳 Docker Deployment

### Pull the published image (GHCR)

LTS images are published to the GitHub Container Registry:

```bash
docker pull ghcr.io/rndmcodeguy20/mpiper:lts          # latest LTS
docker pull ghcr.io/rndmcodeguy20/mpiper:1.0.0-lts    # pinned LTS
docker pull ghcr.io/rndmcodeguy20/mpiper:staging      # latest staging build
```

### Build locally

```bash
# API server
docker build -t mpiper-api:latest -f deploy/docker/mpiper.dockerfile .

# Worker
docker build -t mpiper-worker:latest -f deploy/docker/worker.dockerfile .
```

### Kubernetes

```bash
kubectl apply -f deploy/k8s/
```

## 📖 API Documentation

All `/api/v1` routes require an `Authorization: Bearer <token>` header (see
[Test the API](#6-test-the-api) for how to mint a token).

### Request a presigned upload URL

**Endpoint:** `POST /api/v1/storage/presign`

**Request:**
```json
{
  "fileName": "example.jpg",
  "contentType": "image/jpeg",
  "size": 2048576
}
```

**Response:**
```json
{
  "status": "success",
  "data": {
    "uploadUrl": "http://localhost:9000/...",
    "assetId": "550e8400-e29b-41d4-a716-446655440000",
    "method": "PUT",
    "headers": { "Content-Type": "image/jpeg" },
    "objectPath": "example.jpg",
    "publicUrl": "http://localhost:9000/...",
    "expiresAt": 300
  }
}
```

> The `uploadUrl` / `publicUrl` host comes from the configured storage provider.
> For MinIO it is `S3_PUBLIC_ENDPOINT_URL` (the client-facing endpoint), so the
> URL is reachable from wherever the client runs — see [Storage Providers](#storage-providers).

#### Idempotency

`POST /storage/presign` (and the `complete` endpoint) accept an optional
`Idempotency-Key` header so client retries don't create duplicate assets. The
first request for a given key runs normally and its response is stored
(per-tenant, 24h TTL by default — `IDEMPOTENCY_TTL`); a retry with the **same
key and same body** replays the stored response verbatim (with
`Idempotent-Replayed: true`). Reusing a key with a **different body** returns
`422`, and a duplicate that arrives while the first is still in flight returns
`409`.

```bash
curl -X POST http://localhost:5010/api/v1/storage/presign \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: 9f1c0b2a-..." \
  -H "Content-Type: application/json" \
  -d '{ "fileName": "image.jpg", "contentType": "image/jpeg", "size": 1024000 }'
```

#### Rate limits & quotas

Presign is rate-limited **per tenant** (token bucket, `TENANT_RATE_LIMIT_RPS`
sustained / `TENANT_RATE_LIMIT_BURST` burst); exceeding it returns `429` with a
`Retry-After` header. An optional per-tenant asset quota
(`TENANT_ASSET_QUOTA`, `0` = unlimited) returns `403` once a tenant is at its
cap. Limits are isolated per tenant — one tenant hitting its limit does not
affect another.

### Mark an asset complete (enqueue processing)

**Endpoint:** `GET /api/v1/assets/{assetId}/complete`

Verifies the raw object exists in storage, transitions the asset to `uploaded`,
creates the processing job, and enqueues it (transactionally, via the outbox).

**Response:**
```json
{
  "status": "success",
  "message": "Asset marked as uploaded"
}
```

### Webhooks

Register an endpoint to receive processing-lifecycle events.

**Endpoints:**
- `POST /api/v1/webhooks` — register `{ "url", "secret", "events" }`
- `GET /api/v1/webhooks` — list your registrations
- `DELETE /api/v1/webhooks/{id}` — remove a registration

**Events:** `job.starting`, `job.started`, `job.done`, `job.failed`.

Deliveries are signed: each POST carries an `X-Webhook-Signature: sha256=<hmac>`
header computed over the JSON body using your registration `secret` (stored
encrypted at rest). A background dispatcher delivers pending events **concurrently**
(bounded by `WEBHOOK_CONCURRENCY`) with exponential-backoff retries and tracks them
in the `webhook_deliveries` table.

```bash
curl -X POST http://localhost:5010/api/v1/webhooks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://example.com/hooks/mpiper",
    "secret": "my-signing-secret",
    "events": ["job.starting", "job.started", "job.done", "job.failed"]
  }'
```

## 🎬 Run the demo

[`scripts/demo-e2e.sh`](scripts/demo-e2e.sh) drives the entire pipeline from the
host — exactly like a real client — for both an image and a video, including
webhook delivery. Bring the stack up **with the webhooks overlay**, then run it:

```bash
docker compose -f docker-compose.yml -f docker-compose.webhooks.yml up -d --build

./scripts/demo-e2e.sh
```

For each asset it presigns an upload, PUTs the file straight to MinIO over the
public `localhost:9000` endpoint, marks it complete, waits for the worker to
produce variants, fetches a variant back over HTTP, and asserts the
`job.starting → job.started → job.done` webhooks were delivered. It prints a
PASS/FAIL summary and exits non-zero on any failure.

Requirements on the host: `bash`, `curl`, `jq`, `docker`, and a `python3`
(stdlib only — used to mint an API key seeded into the containerized Postgres).

## 🔧 Development

### Project Structure

```
mpiper/
├── cmd/
│   └── server/          # API server entry point
├── internal/
│   ├── config/          # Configuration management (env-driven singleton)
│   ├── database/        # Postgres pool + embedded migrations
│   ├── handler/         # HTTP handlers
│   ├── metrics/         # OTel metric instruments + provider init
│   ├── middleware/      # HTTP middleware
│   ├── models/          # Request/response models
│   ├── queue/           # Redis Streams producer
│   ├── repository/      # SQL repositories (sqlx)
│   ├── router/          # Route registration
│   ├── server/          # Server setup
│   └── service/         # Business logic
├── pkg/
│   ├── errors/          # Typed API errors
│   └── utils/
│       └── storagex/    # Storage abstraction (GCS, S3/MinIO)
├── worker/
│   ├── consumer/        # Redis Streams consumer (bounded pool, XAUTOCLAIM recovery, DLQ) + config
│   ├── processing/      # Image/video processing
│   ├── storage/         # Storage adapters (base ABC, GCS, S3) + factory
│   └── utils/           # Worker utilities (metrics)
├── db/
│   └── migrations/      # SQL migrations
├── observability/       # OTel collector + Grafana/Tempo/Loki/Prometheus
└── deploy/
    ├── docker/          # Dockerfiles (mpiper, worker)
    └── k8s/             # Kubernetes manifests
```

### Running Tests

**Go tests:**
```bash
task test                      # gotestsum
task test -- ./internal/...     # specific package
task test-coverage              # generates coverage.html
```

**Python tests:**
```bash
poetry run pytest worker/tests/
```

### Build for Production

```bash
# Using Task
task build-prod

# Manual
CGO_ENABLED=0 go build -ldflags="-w -s" -o build/mpiper cmd/server/main.go
```

## 🛠️ Configuration

### Server Configuration

The server is configured via environment variables. See [`internal/config/env.go`](internal/config/env.go) for all available options; worker options live in [`worker/consumer/config.py`](worker/consumer/config.py).

### Storage Providers

MPiper selects a storage backend via `BUCKET_PROVIDER`:

- **Google Cloud Storage (GCS)** - set `GCS_SA_PATH` to a service-account key
- **AWS S3 / S3-compatible (MinIO)** - set the `S3_*` variables; `S3_ENDPOINT_URL` switches the client to path-style addressing for MinIO and other S3-compatible stores
- **Azure Blob Storage** - planned

Both the Go API and the Python worker share the same provider selection and env vars, so a single configuration drives the whole pipeline.

#### Internal vs public endpoints (`S3_PUBLIC_ENDPOINT_URL`)

When the store is reachable by a different host internally than externally —
the classic Docker case, where services talk to `http://minio:9000` but a
browser or a host-run client must use `http://localhost:9000` — set both:

- `S3_ENDPOINT_URL` — the **internal/server-side** endpoint used for object I/O (`http://minio:9000`)
- `S3_PUBLIC_ENDPOINT_URL` — the **client-facing** endpoint baked into presigned upload URLs and persisted variant URLs (`http://localhost:9000`)

This matters because SigV4 signs the `Host` header: a presigned URL must be
generated against the exact host the client will connect to, so it can't simply
be rewritten afterwards. When `S3_PUBLIC_ENDPOINT_URL` is unset it falls back to
`S3_ENDPOINT_URL` (single-endpoint behavior).

### Observability

The API emits OpenTelemetry traces and metrics; the worker exposes Prometheus metrics. The `observability/` directory contains a ready-to-run collector plus Grafana, Tempo, Loki, and Prometheus configuration.

## 📦 Releases

MPiper uses a two-track build pipeline:

- **Staging** — every push to `staging` builds and pushes images tagged `{version}`, `{version}-{sha}`, `{sha}`, and `staging`.
- **LTS** — every push to `master` builds the production long-term-support images tagged `lts`, `{version}-lts`, and `{sha}-lts`.

The version is sourced from the [`.version`](.version) file and embedded into the binary via ldflags (`main.Version`). **v1.0.0** is the initial LTS release — see [Releases](https://github.com/rndmcodeguy20/mpiper/releases).

## 🤝 Contributing

Contributions are welcome! Development happens on `staging`; `master` holds stable LTS releases.

1. Fork the repository
2. Create a feature branch off `staging` (`git checkout -b feat/amazing-feature`)
3. Commit your changes
4. Push the branch and open a Pull Request **against `staging`**

### Development Guidelines

- Write tests for new features
- Follow Go and Python best practices
- Update documentation as needed
- Ensure all tests pass before submitting a PR

## 📝 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 👨‍💻 Author

**Shantanu Mane**
- Website: [rndmcode.in](https://rndmcode.in)
- Email: hi@rndmcode.in
- GitHub: [@rndmcodeguy20](https://github.com/rndmcodeguy20)

## 🙏 Acknowledgments

- Built with [Chi](https://github.com/go-chi/chi) - Lightweight Go router
- Uses [Pillow](https://python-pillow.org/) for image processing
- Powered by [Redis](https://redis.io/) for job queuing
- Data stored in [PostgreSQL](https://www.postgresql.org/)

## 📊 Roadmap

- [x] Support for AWS S3 / MinIO storage
- [x] Webhook delivery with HMAC signing + retry tracking
- [x] Video transcoding with FFmpeg (poster, 720p, preview)
- [x] Concurrent worker pool (`MAX_CONCURRENT_JOBS`) — ~2.4× throughput
- [x] `XAUTOCLAIM` stream recovery + dead-letter stream for poison messages
- [x] Concurrent webhook delivery (`WEBHOOK_CONCURRENCY`)
- [x] End-to-end OpenTelemetry tracing, SLOs, Grafana dashboards + k6 load harness
- [ ] Queue-depth autoscaling (KEDA) — *next*
- [ ] Support for Azure Blob Storage
- [ ] Admin dashboard
- [ ] Batch processing API
- [ ] CDN integration
- [ ] Advanced image optimization (WebP, AVIF)
- [ ] Real-time processing status via WebSockets

## 🐛 Bug Reports & Feature Requests

Please use the [GitHub Issues](https://github.com/rndmcodeguy20/mpiper/issues) page to report bugs or request features.

---

Made with ❤️ by [Shantanu Mane](https://github.com/rndmcodeguy20)
