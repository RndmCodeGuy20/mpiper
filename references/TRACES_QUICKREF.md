# Quick Trace Verification Commands

## One-Line Checks

```powershell
# Check all services are running
docker-compose -f deploy/docker/docker-compose.observability.yml ps

# Test full stack
.\test-traces.ps1

# Generate test traffic
curl http://localhost:8080/api/v1/assets

# Check OTEL Collector is receiving spans
(Invoke-WebRequest http://localhost:8888/metrics).Content | Select-String "otelcol_receiver_accepted_spans"

# Check Tempo has traces
curl "http://localhost:3200/api/search?tags=service.name=mpiper-api" | ConvertFrom-Json | Select-Object -ExpandProperty traces

# Open Grafana Explore with Tempo
Start-Process "http://localhost:3000/explore?left=%7B%22datasource%22:%22tempo%22%7D"
```

## Debugging Steps

```powershell
# 1. Check OTEL Collector logs (last 50 lines)
docker logs mpiper-otel-collector --tail 50

# 2. Check if traces are being exported (real-time)
docker logs mpiper-otel-collector -f | Select-String "otlp/tempo"

# 3. Check application logs for OTEL initialization
docker logs mpiper-api | Select-String "OpenTelemetry"

# 4. View OTEL Collector internal traces
Start-Process "http://localhost:55679/debug/tracez"

# 5. Test Tempo is ready
curl http://localhost:3200/ready

# 6. Check OTEL Collector health
curl http://localhost:13133
```

## TraceQL Examples for Grafana

Open http://localhost:3000/explore, select Tempo, and try:

```traceql
# All traces from your service
{service.name="mpiper-api"}

# Only errors
{service.name="mpiper-api" && status=error}

# Slow requests (>1 second)
{service.name="mpiper-api" && duration > 1s}

# Specific HTTP method
{service.name="mpiper-api" && http.method="POST"}

# Specific route
{service.name="mpiper-api" && http.route="/api/v1/assets"}

# 4xx errors
{service.name="mpiper-api" && http.status_code >= 400 && http.status_code < 500}

# 5xx errors
{service.name="mpiper-api" && http.status_code >= 500}
```

## Port Reference

| Service | Port | Purpose |
|---------|------|---------|
| Grafana | 3000 | Web UI |
| Tempo | 3200 | HTTP API |
| Tempo | 4317 | OTLP gRPC (internal) |
| OTEL Collector | 4319 | OTLP gRPC (host) |
| OTEL Collector | 8888 | Prometheus metrics |
| OTEL Collector | 13133 | Health check |
| OTEL Collector | 55679 | zPages debug |
| Prometheus | 9090 | Web UI |
| Loki | 3100 | HTTP API |

## Common Issues & Fixes

| Issue | Quick Fix |
|-------|-----------|
| No traces in Grafana | Check OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4319 |
| OTEL Collector not receiving | Verify app can reach otel-collector:4317 |
| Traces not in Tempo | Check `docker logs mpiper-otel-collector` for errors |
| Grafana can't connect to Tempo | Verify datasource: http://tempo:3200 |
| Only seeing health check traces | They're filtered - remove filter/healthcheck from pipeline |
