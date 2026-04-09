#!/bin/bash
echo "Stopping GoClaw..."
docker stop goclaw-api goclaw-goclaw-ui-1 goclaw-postgres-1 2>/dev/null || true
echo "GoClaw stopped."
