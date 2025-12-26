# OpenTelemetry Metrics Setup - Summary

## What Was Added

### 1. Metrics Package (`internal/metrics/metrics.go`)
- Added `InitMetrics()` function to initialize OpenTelemetry metrics
- Configured OTLP gRPC exporter to send metrics to OTel Collector
- Created two metrics:
  - `http.server.request.duration` - Histogram tracking request latency
  - `http.server.request.count` - Counter tracking total requests

### 2. Metrics Middleware (`internal/middleware/metrics.go`)
- Created `MetricsMiddleware()` to automatically record metrics for all HTTP requests
- Captures:
  - Request duration in seconds
  - HTTP method
  - HTTP route/path
  - HTTP status code

### 3. Integration
- Updated `cmd/server/main.go` to initialize metrics on startup
- Updated `internal/router/router.go` to use the metrics middleware
- Added dependency: `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc`

### 4. Configuration Updates
- Updated `observability/prometheus.yml` to scrape from correct OTel Collector port (9464)

### 5. Testing Tools
- Created `test-metrics.ps1` - PowerShell script to test the metrics setup
- Created `PROMETHEUS_QUERIES.md` - Reference guide for useful Prometheus queries

## How to Test

### Step 1: Start the observability stack
```powershell
cd deploy/docker
docker compose -f docker-compose.observability.yml up -d
```

### Step 2: Start your API server
```powershell
cd ../..
go run cmd/server/main.go
```

### Step 3: Run the test script
```powershell
./test-metrics.ps1
```

### Step 4: Verify in Prometheus
1. Open Prometheus UI: http://localhost:9090
2. Go to Graph tab
3. Try these queries:
   ```promql
   # See request count
   mpiper_http_server_request_count_total
   
   # See request duration
   mpiper_http_server_request_duration_sum
   
   # Calculate average latency
   rate(mpiper_http_server_request_duration_sum[5m]) / rate(mpiper_http_server_request_duration_count[5m])
   
   # Requests per second
   rate(mpiper_http_server_request_count_total[5m])
   ```

## Expected Metrics in Prometheus

After making requests to your API, you should see these metrics:

```
mpiper_http_server_request_duration_bucket{http_method="GET",http_route="/api/v1/status",http_status_code="200",le="+Inf"}
mpiper_http_server_request_duration_sum{http_method="GET",http_route="/api/v1/status",http_status_code="200"}
mpiper_http_server_request_duration_count{http_method="GET",http_route="/api/v1/status",http_status_code="200"}
mpiper_http_server_request_count_total{http_method="GET",http_route="/api/v1/status",http_status_code="200"}
```

## Troubleshooting

### Metrics not showing up?

1. **Check OTel Collector is running:**
   ```powershell
   curl http://localhost:13133
   ```

2. **Check OTel Collector metrics endpoint:**
   ```powershell
   curl http://localhost:9464/metrics | Select-String "http_server_request"
   ```

3. **Check Prometheus targets:**
   - Open http://localhost:9090/targets
   - Verify `otel-metrics` job is UP and shows `otel-collector:9464`

4. **Check API logs:**
   - Look for "OpenTelemetry metrics initialized successfully" message
   - Look for "OpenTelemetry tracer initialized successfully" message

5. **Generate traffic:**
   - Make some requests: `curl http://localhost:8080/api/v1/status`
   - Wait 15-30 seconds for Prometheus to scrape
   - Query again

### Common Issues

**Issue**: Metrics show up in OTel Collector but not in Prometheus
- **Solution**: Check Prometheus configuration points to correct port (9464)
- **Solution**: Restart Prometheus after config changes

**Issue**: Connection refused errors
- **Solution**: Ensure OTel Collector is running: `docker ps | Select-String otel-collector`
- **Solution**: Check OTEL_EXPORTER_OTLP_ENDPOINT environment variable (default: otel-collector:4317)

**Issue**: No metrics after API requests
- **Solution**: Check API logs for metric initialization errors
- **Solution**: Verify middleware is registered in router
- **Solution**: Wait at least one scrape interval (15 seconds) after making requests

## Architecture Flow

```
Your API (Go)
    |
    | OTLP/gRPC (port 4317)
    v
OTel Collector
    |
    | Prometheus format (port 9464)
    v
Prometheus
    |
    | PromQL queries
    v
Grafana (optional)
```

## Next Steps

1. **Create Grafana Dashboards**: Import or create dashboards using the metrics
2. **Add More Metrics**: Track business metrics, database query times, etc.
3. **Set Up Alerts**: Configure Prometheus alerts for high latency or error rates
4. **Add Custom Labels**: Extend metrics with user IDs, tenant IDs, etc.

## Files Modified/Created

- ✅ `internal/metrics/metrics.go` - NEW
- ✅ `internal/middleware/metrics.go` - NEW
- ✅ `cmd/server/main.go` - MODIFIED
- ✅ `internal/router/router.go` - MODIFIED
- ✅ `observability/prometheus.yml` - MODIFIED
- ✅ `test-metrics.ps1` - NEW
- ✅ `PROMETHEUS_QUERIES.md` - NEW
- ✅ `go.mod` - MODIFIED (added dependency)
