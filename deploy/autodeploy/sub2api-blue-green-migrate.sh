#!/usr/bin/env bash
set -Eeuo pipefail

umask 027

PROGRAM=${0##*/}
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.yml}
LEGACY_COMPOSE_FILE=${LEGACY_COMPOSE_FILE:-}
LEGACY_CONTAINER=${LEGACY_CONTAINER:-sub2api}
GREEN_SERVICE=${GREEN_SERVICE:-sub2api-green}
GREEN_CONTAINER=${GREEN_CONTAINER:-sub2api-green}
GREEN_PORT=${GREEN_PORT:-18081}
WORKER_SERVICE=${WORKER_SERVICE:-product-sync-worker}
WORKER_CONTAINER=${WORKER_CONTAINER:-sub2api-product-sync-worker}
STATE_DIR=${STATE_DIR:-/var/lib/sub2api-autodeploy}
STATE_FILE=${STATE_FILE:-$STATE_DIR/state.env}
LOCK_FILE=${LOCK_FILE:-/run/lock/sub2api-deploy.lock}
HAPROXY_CONFIG_COMMAND=${HAPROXY_CONFIG_COMMAND:-/usr/local/sbin/sub2api-haproxy-config}
BACKUP_COMMAND=${BACKUP_COMMAND:-/usr/local/sbin/sub2api-backup.sh}
HEALTH_TIMEOUT_SECONDS=${HEALTH_TIMEOUT_SECONDS:-180}
STABILITY_SECONDS=${STABILITY_SECONDS:-30}

log() {
  printf '%s [%s] %s\n' "$(date --iso-8601=seconds)" "$PROGRAM" "$*"
}

die() {
  log "ERROR: $*" >&2
  exit 1
}

state_get() {
  local key=$1
  [[ -f "$STATE_FILE" ]] || return 0
  awk -F= -v key="$key" '$1 == key { value = substr($0, length(key) + 2) } END { print value }' "$STATE_FILE"
}

container_image() {
  docker inspect --format '{{.Config.Image}}' "$1" 2>/dev/null || true
}

wait_for_green() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS)) health status successes=0
  while (( SECONDS < deadline )); do
    status=$(docker inspect --format '{{.State.Status}}' "$GREEN_CONTAINER" 2>/dev/null || true)
    health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$GREEN_CONTAINER" 2>/dev/null || true)
    if [[ "$status" == running && "$health" == healthy ]] \
      && curl -fsS --max-time 8 "http://127.0.0.1:$GREEN_PORT/health" >/dev/null 2>&1; then
      ((successes += 1))
      (( successes >= 3 )) && return 0
    else
      successes=0
    fi
    sleep 2
  done
  return 1
}

wait_for_worker() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS)) stable_since=0 status restarting
  while (( SECONDS < deadline )); do
    status=$(docker inspect --format '{{.State.Status}}' "$WORKER_CONTAINER" 2>/dev/null || true)
    restarting=$(docker inspect --format '{{.State.Restarting}}' "$WORKER_CONTAINER" 2>/dev/null || true)
    if [[ "$status" == running && "$restarting" == false ]]; then
      (( stable_since == 0 )) && stable_since=$SECONDS
      (( SECONDS - stable_since >= 15 )) && return 0
    else
      stable_since=0
    fi
    sleep 3
  done
  return 1
}

write_initial_state() {
  local commit=$1 app_image=$2 worker_image=$3
  local previous_commit previous_image previous_worker tmp
  previous_commit=$(state_get PREVIOUS_COMMIT)
  previous_image=$(state_get PREVIOUS_IMAGE)
  previous_worker=$(state_get PREVIOUS_WORKER_IMAGE)
  mkdir -p "$STATE_DIR"
  chmod 700 "$STATE_DIR"
  tmp=$(mktemp "$STATE_DIR/.state.XXXXXX")
  cat >"$tmp" <<EOF
DEPLOYED_COMMIT=$commit
DEPLOYED_IMAGE=$app_image
DEPLOYED_WORKER_IMAGE=$worker_image
ACTIVE_SLOT=green
BLUE_IMAGE=
GREEN_IMAGE=$app_image
PREVIOUS_COMMIT=$previous_commit
PREVIOUS_IMAGE=$previous_image
PREVIOUS_WORKER_IMAGE=$previous_worker
PREVIOUS_SLOT=
DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$STATE_FILE"
}

rollback_legacy() {
  local app_image=$1 worker_image=$2
  log "restoring legacy direct container"
  systemctl stop haproxy.service >/dev/null 2>&1 || true
  docker start "$LEGACY_CONTAINER" >/dev/null 2>&1 || true
  docker stop --time 3 "$GREEN_CONTAINER" >/dev/null 2>&1 || true
  if [[ -n "$LEGACY_COMPOSE_FILE" && -f "$LEGACY_COMPOSE_FILE" ]]; then
    cp -a "$LEGACY_COMPOSE_FILE" "$COMPOSE_FILE"
    (
      cd "$DEPLOY_DIR"
      SUB2API_IMAGE="$app_image" PRODUCT_SYNC_WORKER_IMAGE="$worker_image" \
        docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$WORKER_SERVICE"
    ) || true
  fi
}

[[ $EUID -eq 0 ]] || die "run this migration as root"
[[ -f "$COMPOSE_FILE" ]] || die "Compose file not found: $COMPOSE_FILE"
[[ -x "$HAPROXY_CONFIG_COMMAND" ]] || die "HAProxy helper is not installed"
[[ -x "$BACKUP_COMMAND" ]] || die "backup command is not executable"
docker inspect "$LEGACY_CONTAINER" >/dev/null 2>&1 \
  || die "legacy container not found: $LEGACY_CONTAINER"
[[ "$(docker inspect --format '{{.State.Running}}' "$LEGACY_CONTAINER")" == true ]] \
  || die "legacy container is not running"
curl -fsS --max-time 8 http://127.0.0.1:8080/health >/dev/null \
  || die "legacy application is not healthy"

mkdir -p "$(dirname "$LOCK_FILE")"
exec 9>"$LOCK_FILE"
flock -n 9 || die "another deployment or recovery operation is active"

app_image=$(container_image "$LEGACY_CONTAINER")
worker_image=$(container_image "$WORKER_CONTAINER")
commit=$(state_get DEPLOYED_COMMIT)
[[ -n "$app_image" && -n "$worker_image" && -n "$commit" ]] \
  || die "current images or deployed commit are not recorded"

log "creating final legacy deployment backup"
"$BACKUP_COMMAND"

log "starting prevalidated green slot with $app_image"
(
  cd "$DEPLOY_DIR"
  SUB2API_IMAGE="$app_image" PRODUCT_SYNC_WORKER_IMAGE="$worker_image" \
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$GREEN_SERVICE"
)
if ! wait_for_green; then
  docker logs --tail 200 "$GREEN_CONTAINER" >&2 || true
  docker stop --time 3 "$GREEN_CONTAINER" >/dev/null 2>&1 || true
  die "green slot failed preflight health checks"
fi
curl -fsS --max-time 8 http://127.0.0.1:8080/health >/dev/null \
  || { docker stop --time 3 "$GREEN_CONTAINER" >/dev/null 2>&1 || true; die "legacy app failed after green startup"; }
"$HAPROXY_CONFIG_COMMAND" --validate green

log "handing public port 8080 from the legacy container to HAProxy"
systemctl stop haproxy.service >/dev/null 2>&1 || true
docker stop --time 3 "$LEGACY_CONTAINER" >/dev/null
if ! "$HAPROXY_CONFIG_COMMAND" --activate green; then
  rollback_legacy "$app_image" "$worker_image"
  die "HAProxy failed to take over port 8080"
fi

for _ in $(seq 1 20); do
  curl -fsS --max-time 5 http://127.0.0.1:8080/health >/dev/null 2>&1 && break
  sleep 1
done
if ! curl -fsS --max-time 8 http://127.0.0.1:8080/health >/dev/null; then
  rollback_legacy "$app_image" "$worker_image"
  die "proxy health failed after initial cutover"
fi

log "recreating worker against host.docker.internal:8080"
if ! (
  cd "$DEPLOY_DIR"
  SUB2API_IMAGE="$app_image" PRODUCT_SYNC_WORKER_IMAGE="$worker_image" \
    docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$WORKER_SERVICE"
) || ! wait_for_worker; then
  rollback_legacy "$app_image" "$worker_image"
  die "worker failed after initial cutover"
fi

deadline=$((SECONDS + STABILITY_SECONDS))
while (( SECONDS < deadline )); do
  if ! wait_for_green \
    || ! curl -fsS --max-time 8 http://127.0.0.1:8080/health >/dev/null \
    || [[ "$(docker inspect --format '{{.State.Running}}' "$WORKER_CONTAINER" 2>/dev/null || true)" != true ]]; then
    rollback_legacy "$app_image" "$worker_image"
    die "initial blue/green stack failed its stability window"
  fi
  sleep 2
done

write_initial_state "$commit" "$app_image" "$worker_image"
log "initial cutover succeeded; green is active and the legacy container remains stopped"
