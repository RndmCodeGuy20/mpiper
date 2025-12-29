# OpenTelemetry Observability Stack

This directory contains the production-ready OpenTelemetry Collector configuration for the MPiper media processing service.

## Overview

The observability stack collects and exports three types of telemetry data:
- **Metrics**: Performance and business metrics exported to Prometheus
- **Traces**: Distributed tracing data exported to Tempo
- **Logs**: Application logs exported to Loki

## Architecture

```
┌─────────────────┐
│   Application   │
│   (Go + Python) │
└────────┬────────┘
         │ OTLP (gRPC/HTTP)
         ▼
┌─────────────────┐
│ OTEL Collector  │
│  - Receivers    │
│  - Processors   │
│  - Exporters    │
└────────┬────────┘
         │
    ┌────┴────┬────────┬
    ▼         ▼        ▼
┌────────┐ ┌──────┐ ┌──────┐
│Prometheus│ │Tempo │ │ Loki │
└────────┘ └──────┘ └──────┘
```

## Configuration Files

- **otel-collector.yml**: Main collector configuration
- **README.md**: This documentation file

## Production Deployment Checklist

### 1. Environment Variables

Set these environment variables for production:

```bash
# Required
export DEPLOYMENT_ENV=production
export SERVICE_VERSION=1.0.0
export K8S_CLUSTER_NAME=production-cluster

# Optional (with defaults)
export OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector:4317
export SERVICE_NAME=mpiper-api
export TRACE_SAMPLING_RATE=0.1  # 10% sampling
```

### 2. TLS/Security Configuration

**CRITICAL**: Enable TLS for production deployments.

In `otel-collector.yml`, update the Tempo exporter:

```yaml
exporters:
  otlp/tempo:
    endpoint: tempo:4317
    tls:
      insecure: false  # Change from true
      cert_file: /etc/otel/certs/cert.pem
      key_file: /etc/otel/certs/key.pem
      ca_file: /etc/otel/certs/ca.pem
```

### 3. Resource Limits

Update memory limits based on your traffic volume:

```yaml
processors:
  memory_limiter:
    limit_mib: 2048      # Increase for high-volume
    spike_limit_mib: 512
```

### 4. Sampling Strategy

Adjust sampling rates based on traffic volume and cost constraints:

For traces:
```yaml
processors:
  probabilistic_sampler:
    sampling_percentage: 10.0  # Adjust based on volume
```

For tail sampling (recommended):
```yaml
processors:
  tail_sampling:
    policies:
      - name: probabilistic-policy
        type: probabilistic
        probabilistic: {sampling_percentage: 5}  # Adjust as needed
```

### 5. Batch Sizes

Optimize batch sizes for your workload:

```yaml
processors:
  batch:
    timeout: 10s
    send_batch_size: 2048     # Increase for high throughput
    send_batch_max_size: 4096
```

## Kubernetes Deployment

### ConfigMap

Create a ConfigMap from the collector configuration:

```bash
kubectl create configmap otel-collector-config \
  --from-file=otel-collector.yml \
  -n mpiper
```

### Deployment Example

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: otel-collector
  namespace: mpiper
spec:
  replicas: 2  # For high availability
  selector:
    matchLabels:
      app: otel-collector
  template:
    metadata:
      labels:
        app: otel-collector
    spec:
      containers:
      - name: otel-collector
        image: otel/opentelemetry-collector-contrib:0.91.0
        args:
          - --config=/conf/otel-collector.yml
        ports:
        - containerPort: 4317   # OTLP gRPC
          name: otlp-grpc
        - containerPort: 4318   # OTLP HTTP
          name: otlp-http
        - containerPort: 9464   # Prometheus metrics
          name: prometheus
        - containerPort: 13133  # Health check
          name: health
        - containerPort: 8888   # Internal metrics
          name: internal
        env:
        - name: DEPLOYMENT_ENV
          value: "production"
        - name: SERVICE_VERSION
          value: "1.0.0"
        - name: K8S_CLUSTER_NAME
          value: "prod-cluster"
        volumeMounts:
        - name: config
          mountPath: /conf
        - name: certs  # Mount TLS certificates
          mountPath: /etc/otel/certs
          readOnly: true
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "1Gi"
            cpu: "1000m"
        livenessProbe:
          httpGet:
            path: /health/status
            port: 13133
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /health/status
            port: 13133
          initialDelaySeconds: 10
          periodSeconds: 5
      volumes:
      - name: config
        configMap:
          name: otel-collector-config
      - name: certs
        secret:
          secretName: otel-tls-certs
```

### Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: otel-collector
  namespace: mpiper
spec:
  selector:
    app: otel-collector
  ports:
  - name: otlp-grpc
    port: 4317
    targetPort: 4317
  - name: otlp-http
    port: 4318
    targetPort: 4318
  - name: prometheus
    port: 9464
    targetPort: 9464
  type: ClusterIP
```

## Monitoring the Collector

### Health Checks

The collector exposes a health check endpoint:

```bash
curl http://otel-collector:13133/health/status
```

### Internal Metrics

The collector exposes its own metrics on port 8888:

```bash
curl http://otel-collector:8888/metrics
```

Key metrics to monitor:
- `otelcol_receiver_accepted_spans` - Spans received
- `otelcol_receiver_refused_spans` - Spans rejected
- `otelcol_exporter_sent_spans` - Spans successfully exported
- `otelcol_exporter_send_failed_spans` - Export failures
- `otelcol_processor_batch_batch_send_size` - Batch sizes

### Prometheus ServiceMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: otel-collector
  namespace: mpiper
spec:
  selector:
    matchLabels:
      app: otel-collector
  endpoints:
  - port: prometheus
    interval: 30s
  - port: internal
    interval: 30s
```

## Troubleshooting

### Enable Debug Logging

Temporarily enable debug logging in the collector:

```yaml
service:
  telemetry:
    logs:
      level: debug  # Change from info
```

Or add the logging exporter:

```yaml
exporters:
  logging:
    loglevel: debug

service:
  pipelines:
    traces:
      exporters: [otlp/tempo, logging]  # Add logging
```

### Common Issues

#### High Memory Usage
- Reduce `memory_limiter.limit_mib`
- Decrease batch sizes
- Lower sampling rates
- Increase export frequency

#### Export Failures
- Check network connectivity to backends
- Verify TLS certificates
- Check backend capacity
- Review retry configuration

#### Missing Traces
- Verify sampling configuration
- Check filter processors
- Ensure application is instrumenting correctly
- Review tail sampling policies

## Performance Tuning

### High Throughput (>10k spans/sec)

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        max_concurrent_streams: 200

processors:
  memory_limiter:
    limit_mib: 4096
  batch:
    send_batch_size: 4096
    send_batch_max_size: 8192

exporters:
  otlp/tempo:
    sending_queue:
      num_consumers: 20
      queue_size: 10000
```

### Low Latency Requirements

```yaml
processors:
  batch:
    timeout: 1s  # Reduce from 10s
    send_batch_size: 512
```

### Cost Optimization

```yaml
processors:
  # Aggressive tail sampling
  tail_sampling:
    policies:
      - name: probabilistic-policy
        type: probabilistic
        probabilistic: {sampling_percentage: 1}  # Only 1%
  
  # Filter noisy endpoints
  filter/noise:
    traces:
      span:
        - 'attributes["http.route"] == "/metrics"'
        - 'attributes["http.route"] == "/favicon.ico"'
```

## Security Considerations

1. **Enable TLS** for all exporter connections
2. **Rotate credentials** regularly for backend systems
3. **Remove PII** using attribute processors
4. **Limit network exposure** - don't expose collector ports publicly
5. **Use secrets management** for sensitive configuration
6. **Enable authentication** on receivers if exposed
7. **Audit access** to observability data

## Cost Management

### Estimate Monthly Costs

Approximate formula for trace storage:
```
Monthly Cost = (spans/sec) × (avg_span_size_kb) × (sampling_rate) 
              × (retention_days) × (storage_cost_per_gb) × 2.628M
```

Example: 1000 spans/sec, 2KB avg, 10% sampling, 30 day retention, $0.023/GB:
```
Cost = 1000 × 2 × 0.1 × 30 × 0.023 × 2.628M / 1,000,000
     ≈ $362/month
```

### Cost Reduction Strategies

1. **Tail sampling**: Keep only errors and slow requests
2. **Filter health checks**: Remove noisy endpoints
3. **Reduce retention**: 7-30 days is usually sufficient
4. **Adjust sampling**: Lower rates for high-volume services
5. **Compress data**: Enable gzip compression on exporters

## References

- [OpenTelemetry Collector Documentation](https://opentelemetry.io/docs/collector/)
- [OTLP Protocol Specification](https://opentelemetry.io/docs/specs/otlp/)
- [Collector Configuration Schema](https://opentelemetry.io/docs/collector/configuration/)
- [Processor Documentation](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/processor)
- [Exporter Documentation](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter)
