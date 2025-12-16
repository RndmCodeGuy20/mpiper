#!/bin/bash

echo "🛑 Stopping MPiper Observability Stack..."
docker-compose -f docker-compose.observability.yml down

echo "✅ Stack stopped!"
echo ""
echo "To remove all data volumes, run:"
echo "  docker-compose -f docker-compose.observability.yml down -v"
