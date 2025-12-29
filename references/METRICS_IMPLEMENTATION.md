# Metrics Implementation Summary

This document summarizes the comprehensive observability metrics added to the mpiper service.

## Overview

Metrics have been instrumented across all service layers using OpenTelemetry with OTLP exporter to send data to the configured collector endpoint.

## Go API Server Metrics

### 1. Queue Service (`internal/queue/queue.go`)

**Metrics Added:**
- `QueueMessagePublished` - Counter tracking successfully published messages
- `QueueMessageFailed` - Counter tracking failed message publications
- `QueueProcessingLag` - Histogram measuring message publishing duration

**Labels:**
- `queue.name` - Name of the queue (e.g., "media:jobs")
- `error.type` - Type of error when publishing fails

**Usage:** Tracks Redis stream message publishing operations with success/failure rates and latency.

---

### 2. Asset Service (`internal/service/asset.go`)

**Metrics Added:**
- `AssetUploadTotal` - Counter for asset creation attempts
- `AssetUploadDuration` - Histogram of asset creation duration
- `AssetSizeBytes` - Histogram of asset sizes
- `AssetProcessingTotal` - Counter for assets entering processing
- `AssetProcessingDuration` - Histogram of processing setup time

**Labels:**
- `status` - success/error/processing
- `asset_type` - image/video/audio/document/other
- `error.type` - Type of error when operations fail

**Usage:** Tracks business metrics for asset creation, upload workflows, and processing pipeline entry.

---

### 3. Repository Layer (`internal/repository/asset_repo.go`)

**Metrics Added:**
- `DBQueryTotal` - Counter for all database queries
- `DBQueryErrors` - Counter for failed database queries
- `DBQueryDuration` - Histogram of query execution times

**Labels:**
- `db.operation` - insert/update/select/delete
- `db.table` - assets/jobs/variants
- `db.status` - success/error

**Usage:** Tracks database operations performance and error rates at the repository layer.

---

### 4. Handler Layer (`internal/handler/asset_handler.go`)

**Metrics Added:**
- `HTTPRequestSize` - Histogram of incoming request sizes

**Labels:**
- `http.method` - GET/POST/PUT/DELETE
- `http.route` - API endpoint path

**Usage:** Tracks HTTP request sizes for bandwidth and payload analysis. Complements existing HTTP metrics middleware.

---

### 5. Storage Operations (`pkg/utils/storagex/gcs.go`)

**Metrics Added:**
- `StorageOperationTotal` - Counter for storage operations
- `StorageOperationErrors` - Counter for failed storage operations
- `StorageOperationDuration` - Histogram of storage operation latency

**Labels:**
- `storage.operation` - put/get/get_attrs/delete
- `storage.provider` - gcs/s3/azure
- `storage.status` - success/error

**Usage:** Tracks cloud storage interactions including uploads, downloads, and metadata operations.

---

## Python Worker Metrics

### Worker Service (`worker/utils/metrics.py`)

**New Module Created:** Comprehensive OpenTelemetry metrics initialization for the Python worker.

**Metrics Added:**

#### Queue Metrics
- `mpiper.queue.message.consumed` - Counter for consumed messages
- `mpiper.queue.message.failed` - Counter for failed message processing
- `mpiper.queue.processing.duration` - Histogram of message processing time

#### Job Processing Metrics
- `mpiper.job.processing.total` - Counter for jobs started
- `mpiper.job.processing.success` - Counter for successful job completions
- `mpiper.job.processing.failed` - Counter for failed jobs
- `mpiper.job.processing.duration` - Histogram of job processing time

#### Asset Processing Metrics
- `mpiper.asset.processing.total` - Counter for assets processed
- `mpiper.asset.processing.success` - Counter for successful asset processing
- `mpiper.asset.processing.failed` - Counter for failed asset processing
- `mpiper.asset.processing.duration` - Histogram of asset processing time
- `mpiper.asset.size.bytes` - Histogram of processed asset sizes

#### Storage Metrics
- `mpiper.storage.operation.total` - Counter for storage operations
- `mpiper.storage.operation.errors` - Counter for storage errors
- `mpiper.storage.operation.duration` - Histogram of storage operation time

#### Database Metrics
- `mpiper.db.query.total` - Counter for database queries
- `mpiper.db.query.errors` - Counter for database errors
- `mpiper.db.query.duration` - Histogram of query execution time

**Labels:**
- `status` - success/error/failed
- `queue_name` - Name of the queue being consumed
- `error_type` - Classification of errors
- Various operation-specific labels

---

## Integration Points

### Consumer Updates (`worker/consumer/consumer.py`)

**Changes:**
1. Added time tracking for all operations
2. Integrated metrics recording in `consume()` method
3. Added metrics to `_handle_job()` for job lifecycle tracking
4. Error path metrics for failure analysis

### Main Entry Point (`worker/consumer/main.py`)

**Changes:**
1. Initialize metrics on startup with `init_metrics()`
2. Shutdown metrics gracefully on exit with `shutdown_metrics()`

---

## Configuration

### Environment Variables

Both Go and Python services use the following environment variable:
- `OTEL_EXPORTER_OTLP_ENDPOINT` - OTLP collector endpoint (default: "otel-collector:4317")

Additional service identification:
- `SERVICE_NAME` - Name of the service (default: "mpiper-api" or "mpiper-worker")
- `SERVICE_VERSION` - Version of the service (default: "dev")
- `DEPLOYMENT_ENV` - Environment name (default: "development")

### Export Intervals

- **Go Server:** 15 seconds
- **Python Worker:** 15 seconds

---

## Dependencies Added

### Python (`pyproject.toml`)

```toml
"opentelemetry-api>=1.20.0",
"opentelemetry-sdk>=1.20.0",
"opentelemetry-exporter-otlp-proto-grpc>=1.20.0",
"opentelemetry-instrumentation>=0.41b0",
```

### Go

No new dependencies needed - uses existing OpenTelemetry packages.

---

## Existing Metrics Framework

The following metrics were already defined in `internal/metrics/metrics.go`:

### HTTP Metrics (via middleware)
- `http.server.request.duration`
- `http.server.request.count`
- `http.server.request.size`
- `http.server.response.size`
- `http.server.active_requests`

### System Metrics
- `SystemCPUUsage`
- `SystemMemoryUsage`
- `SystemGoroutineCount`
- `SystemGCPauseDuration`

These are automatically collected and exported alongside the new business and operational metrics.

---

## Observability Stack

The metrics are exported to the existing observability stack:

1. **OTLP Collector** - Receives metrics from both services
2. **Prometheus** - Stores time-series data
3. **Grafana** - Visualizes metrics with dashboards

See `observability/` directory for configuration files and the existing dashboard at `observability/grafana/dashboards/mpiper-http-metrics.json`.

---

## Next Steps

1. **Install Python dependencies:**
   ```bash
   poetry install
   # or
   pip install opentelemetry-api opentelemetry-sdk opentelemetry-exporter-otlp-proto-grpc opentelemetry-instrumentation
   ```

2. **Update Grafana dashboards** to include the new business metrics

3. **Set up alerts** based on error rates and latency thresholds

4. **Monitor metrics** in Prometheus/Grafana to ensure proper collection

---

## Metric Naming Conventions

- **Go metrics:** Follow OpenTelemetry semantic conventions
  - HTTP: `http.server.*`
  - Business: `mpiper.asset.*`, `mpiper.storage.*`
  
- **Python metrics:** Prefixed with service namespace
  - All: `mpiper.*`
  
- **Units:** 
  - Durations: seconds (s)
  - Sizes: bytes (By)
  - Counts: {operation}, {request}, {message}, etc.

---

## Benefits

1. **End-to-end visibility** - Track requests from API to worker completion
2. **Performance monitoring** - Identify bottlenecks in each layer
3. **Error tracking** - Understand failure patterns and rates
4. **Capacity planning** - Monitor resource usage and throughput
5. **SLA compliance** - Track latency and success rates
6. **Business metrics** - Monitor asset processing pipeline health
