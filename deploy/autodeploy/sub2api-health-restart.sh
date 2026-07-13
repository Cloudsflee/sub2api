#!/usr/bin/env bash
set -Eeuo pipefail

HEALTH_URL=${HEALTH_URL:-http://127.0.0.1:8080/health}
LOCK_FILE=${LOCK_FILE:-/run/lock/sub2api-deploy.lock}
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
APP_CONTAINER=${APP_CONTAINER:-sub2api}

mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  exit 0
fi

if curl -fsS --max-time 8 "$HEALTH_URL" >/dev/null; then
  exit 0
fi

logger -t sub2api-health-restart "health check failed; restarting $APP_CONTAINER"
cd "$DEPLOY_DIR"
docker compose restart "$APP_CONTAINER"

for _ in $(seq 1 24); do
  if curl -fsS --max-time 8 "$HEALTH_URL" >/dev/null 2>&1; then
    logger -t sub2api-health-restart "health restored after restart"
    exit 0
  fi
  sleep 5
done

logger -t sub2api-health-restart "health did not recover after restart"
exit 1
