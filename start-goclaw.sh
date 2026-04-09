#!/bin/bash
# Start GoClaw with all dependencies

cd /home/faramix/goclaw

echo "Starting GoClaw..."

# Start postgres if not running
docker start goclaw-postgres-1 2>/dev/null || true

# Start API
docker start goclaw-api 2>/dev/null || \
docker run -d --name goclaw-api \
  --network goclaw_default \
  -p 18790:18790 \
  -v $(pwd)/data:/app/data \
  -e GOCLAW_POSTGRES_DSN="postgres://goclaw:goclaw@postgres:5432/goclaw?sslmode=disable" \
  -e GOCLAW_CONFIG="/app/data/config.json" \
  goclaw-goclaw:latest

# Start Web UI
docker start goclaw-goclaw-ui-1 2>/dev/null || \
docker run -d --name goclaw-goclaw-ui-1 \
  --network goclaw_default \
  -p 3000:80 \
  goclaw-goclaw-ui:latest

sleep 3

echo ""
echo "=== Status ==="
docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep -E "goclaw|postgres"

echo ""
echo "GoClaw API: http://localhost:18790"
echo "Web UI:     http://localhost:3000"
echo ""
echo "Test: curl -X POST http://localhost:18790/v1/chat/completions -H 'Content-Type: application/json' -H 'X-GoClaw-User-Id: test' -d '{\"messages\": [{\"role\": \"user\", \"content\": \"Hoi\"}]}'"
