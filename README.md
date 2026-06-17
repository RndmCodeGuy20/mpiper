# MPiper 🎬

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Release](https://img.shields.io/badge/release-v1.0.0--lts-brightgreen.svg)](https://github.com/rndmcodeguy20/mpiper/releases/tag/v1.0.0)
[![Go Version](https://img.shields.io/badge/Go-1.24-blue.svg)](https://golang.org/)
[![Python Version](https://img.shields.io/badge/Python-3.10+-blue.svg)](https://www.python.org/)

A lightweight, scalable media processing pipeline built with Go and Python. MPiper provides a robust API for uploading media assets and a distributed worker system for processing images and videos with automatic variant generation.

## 🌟 Features

- **RESTful API Server** - High-performance Go server built with Chi router
- **Asynchronous Processing** - Redis Streams job queue for scalable media processing
- **Pluggable Storage** - GCS and S3/MinIO (any S3-compatible store) behind a single provider abstraction, selected by config
- **Image Processing** - Automatic generation of optimized, content-addressed image variants (resize, re-encode, format conversion)
- **Video Processing** - Poster generation, 720p transcode, and preview clips
- **Database-Backed** - PostgreSQL as the durable source of truth for assets, variants, and jobs
- **Webhooks** - Registration and delivery tracking tables for outbound event notifications
- **Observability** - OpenTelemetry tracing + metrics on the API, Prometheus metrics on the worker, with a bundled Grafana/Tempo/Loki/Prometheus stack
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
5. Python worker consumes the job, processes media (resize, transcode, optimize)
6. Variants are written back to object storage (deduplicated by content hash)
7. Database is updated with asset status and variant metadata

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
PORT=8080
LOG_LEVEL=DEBUG

# Database
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=your_password
DB_NAME=mpiper
DB_SSL_MODE=false
AUTO_MIGRATE=true            # run embedded SQL migrations on startup

# Redis (transport for the job stream)
REDIS_CONNECTION_STRING=redis://localhost:6379/0

# Security (must be exactly 32 bytes)
ENCRYPTION_KEY=change_me_to_a_32_byte_secret____

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
S3_ENDPOINT_URL=http://localhost:9000   # set for MinIO / S3-compatible stores

# Worker
STREAM_NAME=media:jobs
JOB_POLL_INTERVAL=1
MAX_CONCURRENT_JOBS=5
```

> The worker reads the same `S3_*` variables as the Go server (falling back to `BUCKET_*`), so one `.env` drives both services.

### 3. Set Up the Database

Migrations run automatically on startup when `AUTO_MIGRATE=true` — both the Go server and the Python worker apply the embedded SQL migrations. To apply them manually instead:

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

```bash
curl -X POST http://localhost:8080/api/v1/assets/upload \
  -H "Content-Type: application/json" \
  -d '{
    "fileName": "image.jpg",
    "contentType": "image/jpeg",
    "size": 1024000
  }'
```

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

### Upload Asset

**Endpoint:** `POST /api/v1/assets/upload`

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
  "uploadUrl": "https://<storage-host>/...",
  "assetId": "550e8400-e29b-41d4-a716-446655440000",
  "method": "PUT",
  "headers": {
    "Content-Type": "image/jpeg"
  },
  "objectPath": "media/raw/550e8400-e29b-41d4-a716-446655440000",
  "publicUrl": "https://<storage-host>/...",
  "expiresAt": 1702468800
}
```

> The `uploadUrl` / `publicUrl` host depends on the configured storage provider (GCS, S3, or a MinIO endpoint).

### Mark Asset as Uploaded

**Endpoint:** `POST /api/v1/assets/{assetId}/uploaded`

**Response:**
```json
{
  "message": "Asset marked as uploaded",
  "assetId": "550e8400-e29b-41d4-a716-446655440000"
}
```

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
│   ├── consumer/        # Redis Streams consumer + config
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
- [x] Webhook delivery tracking (schema)
- [ ] Support for Azure Blob Storage
- [ ] Video transcoding with FFmpeg
- [ ] Admin dashboard
- [ ] Batch processing API
- [ ] CDN integration
- [ ] Advanced image optimization (WebP, AVIF)
- [ ] Real-time processing status via WebSockets

## 🐛 Bug Reports & Feature Requests

Please use the [GitHub Issues](https://github.com/rndmcodeguy20/mpiper/issues) page to report bugs or request features.

---

Made with ❤️ by [Shantanu Mane](https://github.com/rndmcodeguy20)
