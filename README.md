# MPiper 🎬

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.24-blue.svg)](https://golang.org/)
[![Python Version](https://img.shields.io/badge/Python-3.10+-blue.svg)](https://www.python.org/)

A lightweight, scalable media processing pipeline built with Go and Python. MPiper provides a robust API for uploading media assets and a distributed worker system for processing images and videos with automatic variant generation.

## 🌟 Features

- **RESTful API Server** - High-performance Go server built with Chi router
- **Asynchronous Processing** - Redis-based job queue for scalable media processing
- **Multi-Cloud Storage** - Support for Google Cloud Storage (GCS) and AWS S3
- **Image Processing** - Automatic generation of optimized image variants (thumbnails, different formats)
- **Video Processing** - Video transcoding and optimization
- **Database-Backed** - PostgreSQL for reliable metadata and job tracking
- **Docker Ready** - Containerized deployment with Kubernetes support
- **Production Ready** - Structured logging, error handling, and recovery middleware

## 🏗️ Architecture

```
┌─────────────┐         ┌──────────────┐         ┌─────────────┐
│   Client    │────────▶│  Go API      │────────▶│   Redis     │
│             │         │  Server      │         │   Queue     │
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
                        │   Cloud Storage (GCS/S3)         │
                        └──────────────────────────────────┘
```

**Flow:**
1. Client uploads media via REST API
2. Go server generates signed upload URL and creates job
3. Client uploads directly to cloud storage
4. Job is queued in Redis
5. Python worker processes media (resize, transcode, optimize)
6. Variants are stored back to cloud storage
7. Database is updated with asset status and metadata

## 📋 Prerequisites

- **Go** 1.24 or higher
- **Python** 3.10 or higher
- **PostgreSQL** 12 or higher
- **Redis** 6 or higher
- **Task** (optional, for build automation) - [Installation guide](https://taskfile.dev/installation/)
- Cloud storage account (GCS or AWS S3)

## 🚀 Quick Start

### 1. Clone the Repository

```bash
git clone https://github.com/rndmcodeguy20/mpiper.git
cd mpiper
```

### 2. Configure Environment

Create a `.env` file in the project root:

```env
# Server Configuration
SERVER_HOST=localhost
SERVER_PORT=8080
ENV=development

# Database
DB_HOST=localhost
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=your_password
DB_NAME=mpiper
DB_SSLMODE=disable

# Redis
REDIS_HOST=localhost
REDIS_PORT=6379
REDIS_PASSWORD=
REDIS_DB=0

# Storage (GCS)
STORAGE_PROVIDER=gcp
GCS_BUCKET=your-bucket-name
GCS_CREDENTIALS_PATH=.secrets/service-account.json

# Worker
TEMP_DIR=/tmp/mpiper
STREAM_NAME=media:jobs
JOB_POLL_INTERVAL=1
```

### 3. Set Up Database

```bash
# Create database
createdb mpiper

# Run migrations
psql -d mpiper -f db/migrations/001_seed.sql
```

### 4. Install Dependencies

**Go Server:**
```bash
go mod download
```

**Python Worker:**
```bash
pip install poetry
poetry install
```

Or using pip directly:
```bash
pip install -r requirements.txt
```

### 5. Run the Services

**Option A: Using Task (Recommended)**

```bash
# Run API server
task dev

# Run worker (in another terminal)
poetry run python -m worker
```

**Option B: Manual**

```bash
# Run API server
go run cmd/server/main.go

# Run worker
python -m worker
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

### Build Images

```bash
# Build API server
docker build -t mpiper-api:latest -f deploy/docker/mpiper.dockerfile .

# Build worker
docker build -t mpiper-worker:latest -f deploy/docker/worker.dockerfile .
```

### Run with Docker Compose

```bash
docker-compose up -d
```

### Kubernetes Deployment

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
  "uploadUrl": "https://storage.googleapis.com/...",
  "assetId": "550e8400-e29b-41d4-a716-446655440000",
  "method": "PUT",
  "headers": {
    "Content-Type": "image/jpeg"
  },
  "objectPath": "media/raw/550e8400-e29b-41d4-a716-446655440000",
  "publicUrl": "https://storage.googleapis.com/...",
  "expiresAt": 1702468800
}
```

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
│   ├── config/          # Configuration management
│   ├── database/        # Database connections
│   ├── handler/         # HTTP handlers
│   ├── middleware/      # HTTP middleware
│   ├── models/          # Data models
│   ├── queue/           # Redis queue implementation
│   ├── repository/      # Database repositories
│   ├── router/          # Route definitions
│   ├── server/          # Server setup
│   └── service/         # Business logic
├── pkg/
│   ├── errors/          # Error handling
│   └── utils/           # Utility functions
├── worker/
│   ├── consumer/        # Job consumer
│   ├── processing/      # Media processing logic
│   ├── storage/         # Storage adapters
│   └── utils/           # Worker utilities
├── db/
│   └── migrations/      # SQL migrations
└── deploy/
    ├── docker/          # Docker files
    └── k8s/             # Kubernetes manifests
```

### Running Tests

**Go tests:**
```bash
go test ./...
```

**Python tests:**
```bash
poetry run pytest
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

The server can be configured via environment variables or a configuration file. See [`internal/config/env.go`](internal/config/env.go) for all available options.

### Storage Providers

MPiper supports multiple cloud storage providers:

- **Google Cloud Storage (GCS)** - Default, recommended for production
- **AWS S3** - Coming soon
- **Azure Blob Storage** - Coming soon

### Worker Configuration

Configure worker behavior in `worker/consumer/config.py`:

- Processing pipelines (image/video)
- Variant generation rules
- Storage destinations
- Concurrency settings

## 🤝 Contributing

Contributions are welcome! Please follow these steps:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Development Guidelines

- Write tests for new features
- Follow Go and Python best practices
- Update documentation as needed
- Ensure all tests pass before submitting PR

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

- [ ] Support for AWS S3 storage
- [ ] Support for Azure Blob Storage
- [ ] Video transcoding with FFmpeg
- [ ] Webhook notifications
- [ ] Admin dashboard
- [ ] Batch processing API
- [ ] CDN integration
- [ ] Advanced image optimization (WebP, AVIF)
- [ ] Real-time processing status via WebSockets

## 🐛 Bug Reports & Feature Requests

Please use the [GitHub Issues](https://github.com/rndmcodeguy20/mpiper/issues) page to report bugs or request features.

## 📚 Additional Resources

- [Go Documentation](https://golang.org/doc/)
- [Python Documentation](https://docs.python.org/)
- [PostgreSQL Documentation](https://www.postgresql.org/docs/)
- [Redis Documentation](https://redis.io/documentation)
- [Task Documentation](https://taskfile.dev/)

---

Made with ❤️ by [Shantanu Mane](https://github.com/rndmcodeguy20)

