# OpenTelemetry Metrics - Prometheus Queries

This document contains useful Prometheus queries for monitoring your HTTP request metrics.

## Available Metrics

### 1. HTTP Request Duration (Histogram)
- **Metric Name**: `mpiper_http_server_request_duration`
- **Type**: Histogram
- **Unit**: seconds
- **Labels**: 
  - `http_method` - HTTP method (GET, POST, etc.)
  - `http_route` - Request path/route
  - `http_status_code` - HTTP response status code

### 2. HTTP Request Count (Counter)
- **Metric Name**: `mpiper_http_server_request_count`
- **Type**: Counter
- **Unit**: requests
- **Labels**: Same as duration metric

## Useful Prometheus Queries

### Basic Queries

#### View all request durations
```promql
mpiper_http_server_request_duration_sum
```

#### View total request count
```promql
mpiper_http_server_request_count_total
```

#### View request count by route
```promql
mpiper_http_server_request_count_total{http_route="/api/v1/status"}
```

### Rate Queries

#### Requests per second (last 5 minutes)
```promql
rate(mpiper_http_server_request_count_total[5m])
```

#### Requests per second by HTTP method
```promql
sum by(http_method) (rate(mpiper_http_server_request_count_total[5m]))
```

#### Requests per second by route
```promql
sum by(http_route) (rate(mpiper_http_server_request_count_total[5m]))
```

### Latency Queries

#### Average request duration (last 5 minutes)
```promql
rate(mpiper_http_server_request_duration_sum[5m]) / rate(mpiper_http_server_request_duration_count[5m])
```

#### Average request duration by route
```promql
sum by(http_route) (rate(mpiper_http_server_request_duration_sum[5m])) / sum by(http_route) (rate(mpiper_http_server_request_duration_count[5m]))
```

#### P95 latency (95th percentile)
```promql
histogram_quantile(0.95, rate(mpiper_http_server_request_duration_bucket[5m]))
```

#### P99 latency (99th percentile)
```promql
histogram_quantile(0.99, rate(mpiper_http_server_request_duration_bucket[5m]))
```

### Error Rate Queries

#### Error rate (4xx and 5xx responses)
```promql
sum(rate(mpiper_http_server_request_count_total{http_status_code=~"4..|5.."}[5m])) / sum(rate(mpiper_http_server_request_count_total[5m]))
```

#### 5xx error rate
```promql
sum(rate(mpiper_http_server_request_count_total{http_status_code=~"5.."}[5m])) / sum(rate(mpiper_http_server_request_count_total[5m]))
```

#### Request count by status code
```promql
sum by(http_status_code) (rate(mpiper_http_server_request_count_total[5m]))
```

### Advanced Queries

#### Top 5 slowest endpoints
```promql
topk(5, sum by(http_route) (rate(mpiper_http_server_request_duration_sum[5m])) / sum by(http_route) (rate(mpiper_http_server_request_duration_count[5m])))
```

#### Requests per minute by status code
```promql
sum by(http_status_code) (rate(mpiper_http_server_request_count_total[1m])) * 60
```

#### Total data transfer time (sum of all request durations)
```promql
sum(mpiper_http_server_request_duration_sum)
```

#### Throughput (requests per second) across all endpoints
```promql
sum(rate(mpiper_http_server_request_count_total[5m]))
```

## Testing Your Setup

### 1. Check if metrics are being collected
```promql
up{job="otel-metrics"}
```
Should return `1` if the OTel Collector is up.

### 2. Check for any HTTP metrics
```promql
{__name__=~"mpiper_.*"}
```
Lists all metrics with the `mpiper_` prefix.

### 3. View OTel Collector's own metrics
```promql
{job="otel-collector"}
```

## Grafana Dashboard Ideas

You can create Grafana panels using these queries:

1. **Request Rate Panel** - Time series graph
   - Query: `sum(rate(mpiper_http_server_request_count_total[5m]))`
   - Title: "Requests per Second"

2. **Average Latency Panel** - Time series graph
   - Query: `rate(mpiper_http_server_request_duration_sum[5m]) / rate(mpiper_http_server_request_duration_count[5m])`
   - Title: "Average Response Time"

3. **Status Code Distribution** - Pie chart
   - Query: `sum by(http_status_code) (increase(mpiper_http_server_request_count_total[5m]))`
   - Title: "Response Status Codes"

4. **Endpoint Performance** - Table
   - Query: `sum by(http_route, http_method) (rate(mpiper_http_server_request_duration_sum[5m])) / sum by(http_route, http_method) (rate(mpiper_http_server_request_duration_count[5m]))`
   - Title: "Endpoint Average Duration"

## Troubleshooting

### Metrics not appearing?

1. Check OTel Collector is receiving metrics:
   ```bash
   curl http://localhost:9464/metrics | grep http_server_request
   ```

2. Check Prometheus targets:
   - Open http://localhost:9090/targets
   - Verify `otel-metrics` target is UP

3. Check your API is sending metrics:
   - Make some requests to your API
   - Wait 15-30 seconds for scrape interval
   - Query again in Prometheus

4. Check OTel Collector logs:
   ```bash
   docker logs otel-collector
   ```
