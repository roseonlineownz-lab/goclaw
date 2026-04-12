#!/usr/bin/env bash

log() {
  echo "[GoClaw] $1"
}

container_exists() {
  docker container inspect "$1" >/dev/null 2>&1
}

image_exists() {
  docker image inspect "$1" >/dev/null 2>&1
}

ensure_network() {
  if docker network inspect goclaw_default >/dev/null 2>&1; then
    return
  fi

  if docker network create goclaw_default >/dev/null 2>&1; then
    log "Created docker network goclaw_default."
  else
    log "Could not create docker network goclaw_default."
  fi
}

cd /home/faramix/goclaw || {
  log "Repo path missing; skipping startup."
  exit 0
}

if ! command -v docker >/dev/null 2>&1; then
  log "docker is not available in PATH; skipping startup."
  exit 0
fi

if ! docker info >/dev/null 2>&1; then
  log "docker daemon is unavailable; skipping startup."
  exit 0
fi

log "Starting GoClaw..."
ensure_network

if container_exists goclaw-postgres-1; then
  docker start goclaw-postgres-1 >/dev/null 2>&1 || log "Failed to start goclaw-postgres-1."
else
  log "Container goclaw-postgres-1 does not exist; skipping postgres."
fi

if container_exists goclaw-api; then
  docker start goclaw-api >/dev/null 2>&1 || log "Failed to start goclaw-api."
elif image_exists goclaw-goclaw:latest; then
  docker run -d --restart unless-stopped --name goclaw-api \
    --network goclaw_default \
    -p 18790:18790 \
    -v "$(pwd)/data:/app/data" \
    -e GOCLAW_POSTGRES_DSN="postgres://goclaw:goclaw@postgres:5432/goclaw?sslmode=disable" \
    -e GOCLAW_CONFIG="/app/data/config.json" \
    goclaw-goclaw:latest >/dev/null 2>&1 || log "Failed to create goclaw-api."
else
  log "Image goclaw-goclaw:latest is missing; skipping API."
fi

if container_exists goclaw-goclaw-ui-1; then
  docker start goclaw-goclaw-ui-1 >/dev/null 2>&1 || log "Failed to start goclaw-goclaw-ui-1."
elif image_exists goclaw-goclaw-ui:latest; then
  docker run -d --restart unless-stopped --name goclaw-goclaw-ui-1 \
    --network goclaw_default \
    -p 3000:80 \
    goclaw-goclaw-ui:latest >/dev/null 2>&1 || log "Failed to create goclaw-goclaw-ui-1."
else
  log "Image goclaw-goclaw-ui:latest is missing; skipping web UI."
fi

sleep 2

echo ""
echo "=== Status ==="
status_output="$(docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" | grep -E "goclaw|postgres" || true)"
if [ -n "$status_output" ]; then
  echo "$status_output"
else
  log "No GoClaw containers are currently running."
fi

echo ""
echo "GoClaw API: http://localhost:18790"
echo "Web UI:     http://localhost:3000"
echo ""
echo "Test: curl -X POST http://localhost:18790/v1/chat/completions -H 'Content-Type: application/json' -H 'X-GoClaw-User-Id: test' -d '{\"messages\": [{\"role\": \"user\", \"content\": \"Hoi\"}]}'"
exit 0
