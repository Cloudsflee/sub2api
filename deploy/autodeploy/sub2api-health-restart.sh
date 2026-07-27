#!/usr/bin/env bash
set -Eeuo pipefail

PROXY_HEALTH_URL=${PROXY_HEALTH_URL:-${HEALTH_URL:-http://127.0.0.1:8080/health}}
LOCK_FILE=${LOCK_FILE:-/run/lock/sub2api-deploy.lock}
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.yml}
STATE_FILE=${STATE_FILE:-/var/lib/sub2api-autodeploy/state.env}
BLUE_SERVICE=${BLUE_SERVICE:-sub2api-blue}
GREEN_SERVICE=${GREEN_SERVICE:-sub2api-green}
BLUE_CONTAINER=${BLUE_CONTAINER:-sub2api-blue}
GREEN_CONTAINER=${GREEN_CONTAINER:-sub2api-green}
BLUE_PORT=${BLUE_PORT:-18080}
GREEN_PORT=${GREEN_PORT:-18081}
HAPROXY_CONFIG_COMMAND=${HAPROXY_CONFIG_COMMAND:-/usr/local/sbin/sub2api-haproxy-config}
HAPROXY_SERVICE=${HAPROXY_SERVICE:-haproxy}
RECOVERY_TIMEOUT_SECONDS=${RECOVERY_TIMEOUT_SECONDS:-180}
HEALTH_CONSECUTIVE_SUCCESSES=${HEALTH_CONSECUTIVE_SUCCESSES:-3}

state_get() {
  local key=$1
  [[ -f "$STATE_FILE" ]] || return 0
  awk -F= -v key="$key" '$1 == key { value = substr($0, length(key) + 2) } END { print value }' "$STATE_FILE"
}

valid_slot() {
  [[ "$1" == blue || "$1" == green ]]
}

slot_service() {
  [[ "$1" == blue ]] && printf '%s\n' "$BLUE_SERVICE" || printf '%s\n' "$GREEN_SERVICE"
}

slot_container() {
  [[ "$1" == blue ]] && printf '%s\n' "$BLUE_CONTAINER" || printf '%s\n' "$GREEN_CONTAINER"
}

slot_port() {
  [[ "$1" == blue ]] && printf '%s\n' "$BLUE_PORT" || printf '%s\n' "$GREEN_PORT"
}

slot_image() {
  local key
  [[ "$1" == blue ]] && key=BLUE_IMAGE || key=GREEN_IMAGE
  state_get "$key"
}

resolve_active_slot() {
  local configured recorded
  configured=$("$HAPROXY_CONFIG_COMMAND" --current 2>/dev/null || true)
  recorded=$(state_get ACTIVE_SLOT)
  if valid_slot "$configured"; then
    printf '%s\n' "$configured"
  elif valid_slot "$recorded"; then
    printf '%s\n' "$recorded"
  else
    return 1
  fi
}

slot_healthy() {
  local container port health
  container=$(slot_container "$1")
  port=$(slot_port "$1")
  health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null || true)
  [[ "$(docker inspect --format '{{.State.Running}}' "$container" 2>/dev/null || true)" == true ]] \
    && [[ "$health" == healthy ]] \
    && curl -fsS --max-time 8 "http://127.0.0.1:$port/health" >/dev/null
}

proxy_healthy() {
  systemctl is-active --quiet "$HAPROXY_SERVICE" \
    && curl -fsS --max-time 8 "$PROXY_HEALTH_URL" >/dev/null
}

wait_for_slot() {
  local slot=$1 deadline=$((SECONDS + RECOVERY_TIMEOUT_SECONDS)) successes=0
  while (( SECONDS < deadline )); do
    if slot_healthy "$slot"; then
      ((successes += 1))
      (( successes >= HEALTH_CONSECUTIVE_SUCCESSES )) && return 0
    else
      successes=0
    fi
    sleep 2
  done
  return 1
}

mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  exit 0
fi

active_slot=$(resolve_active_slot || true)
if ! valid_slot "$active_slot"; then
  logger -t sub2api-health-restart "cannot determine active blue/green slot"
  exit 1
fi

if slot_healthy "$active_slot" && proxy_healthy; then
  exit 0
fi

active_container=$(slot_container "$active_slot")
active_service=$(slot_service "$active_slot")
active_image=$(slot_image "$active_slot")
[[ -n "$active_image" ]] || active_image=$(state_get DEPLOYED_IMAGE)

if ! slot_healthy "$active_slot"; then
  logger -t sub2api-health-restart \
    "slot $active_slot is unhealthy; recreating $active_container"
  [[ -n "$active_image" ]] || {
    logger -t sub2api-health-restart "no image recorded for slot $active_slot"
    exit 1
  }
  cd "$DEPLOY_DIR"
  SUB2API_IMAGE="$active_image" \
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$active_service"
  if ! wait_for_slot "$active_slot"; then
    logger -t sub2api-health-restart \
      "slot $active_slot did not recover within ${RECOVERY_TIMEOUT_SECONDS}s"
    exit 1
  fi
fi

logger -t sub2api-health-restart "activating HAProxy for healthy slot $active_slot"
if ! "$HAPROXY_CONFIG_COMMAND" --activate "$active_slot"; then
  logger -t sub2api-health-restart "HAProxy activation failed for slot $active_slot"
  exit 1
fi

for _ in $(seq 1 12); do
  if proxy_healthy; then
    logger -t sub2api-health-restart "HAProxy and slot $active_slot are healthy"
    exit 0
  fi
  sleep 2
done

logger -t sub2api-health-restart "proxy health did not recover for slot $active_slot"
exit 1
