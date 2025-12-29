# Trace Verification Guide for MPiper

This guide will help you verify that OpenTelemetry traces are being correctly sent to Tempo and displayed in Grafana.

## Quick Check

Run the automated test script:
```powershell
.\test-traces.ps1
```

## Manual Verification Steps

### 1. Verify Observability Stack is Running

Check all services are up:
```powershell
docker-compose -f deploy/docker/docker-compose.observability.yml ps
```

Expected services:
- ✅ mpiper-grafana
- ✅ mpiper-tempo
- ✅ mpiper-otel-collector
- ✅ mpiper-prometheus
- ✅ mpiper-loki

If not running:
```powershell
docker-compose -f deploy/docker/docker-compose.observability.yml up -d
```

### 2. Check OTEL Collector is Receiving Traces

#### View OTEL Collector Logs
```powershell
docker logs mpiper-otel-collector --tail 50 -f
```

Look for messages like:
```
Traces	{"kind": "exporter", "data_type": "traces", "name": "otlp/tempo"}
```

#### Check OTEL Collector Metrics
Open: http://localhost:8888/metrics

Search for these metrics:
- `otelcol_receiver_accepted_spans` - Spans received from your app
- `otelcol_exporter_sent_spans{exporter="otlp/tempo"}` - Spans sent to Tempo
- `otelcol_receiver_refused_spans` - Should be 0

#### Use OTEL Collector zPages
Open: http://localhost:55679/debug/tracez

This shows:
- Active spans being processed
- Export pipeline statistics
- Error rates

### 3. Verify Application is Sending Traces

#### Check Application Logs
Look for initialization message:
```
OpenTelemetry tracer initialized successfully
```

#### Environment Variables Check
Ensure your application has:
```env
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4319
SERVICE_NAME=mpiper-api
SERVICE_VERSION=dev
DEPLOYMENT_ENV=development
```

Note: Port 4319 on host maps to 4317 in the container (see docker-compose.observability.yml)

#### Test with curl
```powershell
curl http://localhost:8080/api/v1/assets -v
```

Check response headers for trace propagation (optional but good to have):
```
traceparent: 00-<trace-id>-<span-id>-01
```

### 4. Query Tempo Directly

#### Check Tempo Health
```powershell
curl http://localhost:3200/ready
```

Should return: `ready`

#### Search for Traces
```powershell
# Get traces from last 1 hour for mpiper-api service
$start = (Get-Date).AddHours(-1).ToUniversalTime().ToString('o')
$end = (Get-Date).ToUniversalTime().ToString('o')
curl "http://localhost:3200/api/search?tags=service.name=mpiper-api&start=$start&end=$end"
```

#### Get Specific Trace
If you have a trace ID:
```powershell
curl http://localhost:3200/api/traces/<trace-id>
```

### 5. View Traces in Grafana

#### Access Grafana
1. Open: http://localhost:3000
2. Login: `admin` / `admin` (change in production!)

#### Navigate to Explore
1. Click **Explore** (compass icon) in left sidebar
2. Select **Tempo** from datasource dropdown (top)

#### Search Methods

##### Method 1: TraceQL Query
In the query editor, enter:
```traceql
{service.name="mpiper-api"}
```

Advanced queries:
```traceql
# Find traces with errors
{service.name="mpiper-api" && status=error}

# Find traces with specific HTTP method
{service.name="mpiper-api" && http.method="POST"}

# Find slow traces (duration > 1s)
{service.name="mpiper-api" && duration > 1s}

# Combine conditions
{service.name="mpiper-api" && http.method="GET" && http.status_code=200}
```

##### Method 2: Search Interface
1. Click **Search** tab
2. Enter search criteria:
   - **Service Name:** mpiper-api
   - **Time Range:** Last 15 minutes
3. Click **Run query**

##### Method 3: Service Graph
1. In Explore, select **Tempo** datasource
2. Click **Service Graph** tab
3. View service dependencies and request rates

#### Interpret Trace View

When you click on a trace, you'll see:
- **Flame Graph:** Visual representation of spans
- **Span Duration:** Time taken by each operation
- **Attributes:** HTTP method, status code, route, etc.
- **Events:** Log events within spans (if added)
- **Errors:** Red highlighted spans indicate errors

### 6. Troubleshooting

#### No Traces Appearing?

**Check 1: Is application sending traces?**
```powershell
# Check if InitTracer was called
docker logs mpiper-api | Select-String "OpenTelemetry"
```

**Check 2: Is OTEL Collector receiving them?**
```powershell
docker logs mpiper-otel-collector | Select-String "traces"
```

**Check 3: Network connectivity**
```powershell
# From your application container, test connectivity
docker exec mpiper-api ping otel-collector
docker exec mpiper-api telnet otel-collector 4317
```

**Check 4: OTEL Collector configuration**
```powershell
# Verify the debug exporter is enabled (for testing)
docker exec mpiper-otel-collector cat /etc/otel-collector.yaml | Select-String "debug"
```

If debug exporter is enabled, you should see detailed span information in collector logs:
```powershell
docker logs mpiper-otel-collector 2>&1 | Select-String "Span"
```

**Check 5: Tempo connectivity**
```powershell
# Check if Tempo is reachable from OTEL Collector
docker exec mpiper-otel-collector ping tempo
```

#### Traces Partially Missing?

This could be due to sampling. Check your sampling configuration in [otel.go](internal/metrics/otel.go):

```go
// Development environment always samples
if env == "development" || env == "dev" || env == "" {
    return sdktrace.AlwaysSample()
}
```

#### High Latency or Delayed Traces?

Check OTEL Collector batch processor settings in [otel-collector.yml](observability/otel-collector.yml):

```yaml
batch:
  timeout: 5s              # Increase if traces arrive too slowly
  send_batch_size: 1024
  send_batch_max_size: 2048
```

#### Traces Not Correlating with Logs?

Ensure trace context is being logged. Check [logger.go](pkg/utils/logger.go) for trace ID extraction:

```go
// Your logs should include trace_id field
logger.Info("Processing request", 
    zap.String("trace_id", span.SpanContext().TraceID().String()),
)
```

## Advanced: Custom Instrumentation

### Adding Spans in Your Code

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/codes"
)

func YourFunction(ctx context.Context) error {
    tracer := otel.Tracer("mpiper-api")
    
    // Start a new span
    ctx, span := tracer.Start(ctx, "YourFunction")
    defer span.End()
    
    // Add attributes
    span.SetAttributes(
        attribute.String("user.id", userID),
        attribute.Int("items.count", len(items)),
    )
    
    // Do work...
    err := doSomething(ctx)
    
    // Record error if any
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        return err
    }
    
    span.SetStatus(codes.Ok, "Success")
    return nil
}
```

### Adding Events to Spans

```go
span.AddEvent("cache.hit", trace.WithAttributes(
    attribute.String("cache.key", key),
))
```

### Linking Spans Across Services

When making HTTP requests to other services:

```go
import (
    "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Use instrumented HTTP client
client := &http.Client{
    Transport: otelhttp.NewTransport(http.DefaultTransport),
}

// Trace context will be automatically propagated via headers
resp, err := client.Do(req.WithContext(ctx))
```

## Performance Considerations

### Production Sampling

For high-traffic production environments, use ratio-based sampling:

```go
// In otel.go, modify getSampler() for production
return sdktrace.ParentBased(
    sdktrace.TraceIDRatioBased(0.01), // Sample 1% of traces
)
```

Or set via environment:
```env
TRACE_SAMPLING_RATE=0.01
```

### Span Limits

Current limits (from otel.go):
- Max attributes per span: 128
- Max events per span: 128
- Max attribute value length: 4096

Adjust if needed for your use case.

## Useful Grafana Queries

### View HTTP Request Latency Distribution
In Explore → Tempo:
```traceql
{service.name="mpiper-api" && span.http.method="GET"} 
| histogram() by duration
```

### Find Error Traces
```traceql
{service.name="mpiper-api" && status=error}
```

### Traces by Route
```traceql
{service.name="mpiper-api" && http.route="/api/v1/assets"}
```

## Dashboard Setup

Import pre-built dashboard:
1. Go to Dashboards → Import
2. Upload: `observability/grafana/dashboards/mpiper-http-metrics.json`
3. This includes RED metrics (Rate, Errors, Duration) with trace links

## Health Check Exclusion

Health check endpoints are automatically filtered out to reduce noise. See [otel-collector.yml](observability/otel-collector.yml):

```yaml
filter/healthcheck:
  traces:
    span:
      - 'attributes["http.route"] == "/health"'
      - 'attributes["http.route"] == "/healthz"'
```

To include them, comment out the filter in the traces pipeline.

## References

- [OpenTelemetry Go Docs](https://opentelemetry.io/docs/instrumentation/go/)
- [Tempo Documentation](https://grafana.com/docs/tempo/latest/)
- [TraceQL Language](https://grafana.com/docs/tempo/latest/traceql/)
- [OTEL Collector Configuration](https://opentelemetry.io/docs/collector/configuration/)
