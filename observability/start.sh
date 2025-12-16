#!/bin/bash

# ============================================================================
# Quick Start Script for MPiper Observability Stack
# ============================================================================

set -e

echo "🚀 Starting MPiper Observability Stack..."
echo ""

# Create required directories
echo "📁 Creating directories..."
mkdir -p observability/grafana/datasources
mkdir -p observability/grafana/dashboards/json

# Start the stack
echo "🐳 Starting Docker Compose..."
docker-compose -f ../deploy/docker/docker-compose.observability.yml up -d

echo ""
echo "⏳ Waiting for services to be healthy..."
sleep 10

# Check health
echo "🏥 Checking service health..."
docker-compose -f docker-compose.observability.yml ps

echo ""
echo "✅ Observability Stack Started!"
echo ""
echo "📊 Access Points:"
echo "  - Grafana:        http://localhost:3000 (admin/admin)"
echo "  - Prometheus:     http://localhost:9090"
echo "  - Tempo:          http://localhost:3200"
echo "  - Loki:           http://localhost:3100"
echo "  - OTEL Collector: http://localhost:13133/health/status"
echo ""
echo "🔧 Configuration:"
echo "  Set these environment variables in your app:"
echo "  export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4319"
echo "  export SERVICE_NAME=mpiper-api"
echo "  export SERVICE_VERSION=0.1.0"
echo "  export DEPLOYMENT_ENV=development"
echo ""
echo "📝 Next Steps:"
echo "  1. Run your MPiper API"
echo "  2. Generate some traffic"
echo "  3. Open Grafana and explore traces in Tempo"
echo ""
