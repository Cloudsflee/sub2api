#!/usr/bin/env bash
set -Eeuo pipefail

umask 027

PROGRAM=${0##*/}
MODE=${1:---deploy}

REPO_DIR=${REPO_DIR:-/opt/sub2api-custom-src}
BUILD_DIR=${BUILD_DIR:-/opt/sub2api-release}
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.yml}
REMOTE=${REMOTE:-origin}
SOURCE_BRANCH=${SOURCE_BRANCH:-custom}
DEPLOY_BRANCH=${DEPLOY_BRANCH:-deploy/custom}
APP_SERVICE=${APP_SERVICE:-sub2api}
APP_CONTAINER=${APP_CONTAINER:-sub2api}
WORKER_SERVICE=${WORKER_SERVICE:-product-sync-worker}
WORKER_CONTAINER=${WORKER_CONTAINER:-sub2api-product-sync-worker}
APP_IMAGE_REPOSITORY=${APP_IMAGE_REPOSITORY:-sub2api-custom}
WORKER_IMAGE_REPOSITORY=${WORKER_IMAGE_REPOSITORY:-sub2api-product-sync-worker}
HEALTH_URL=${HEALTH_URL:-http://127.0.0.1:8080/health}
HEALTH_TIMEOUT_SECONDS=${HEALTH_TIMEOUT_SECONDS:-180}
WORKER_STABLE_SECONDS=${WORKER_STABLE_SECONDS:-15}
BACKUP_BEFORE_DEPLOY=${BACKUP_BEFORE_DEPLOY:-true}
BACKUP_COMMAND=${BACKUP_COMMAND:-/usr/local/sbin/sub2api-backup.sh}
MANAGE_WORKER=${MANAGE_WORKER:-true}
LOCK_FILE=${LOCK_FILE:-/run/lock/sub2api-deploy.lock}
STATE_DIR=${STATE_DIR:-/var/lib/sub2api-autodeploy}
STATE_FILE=${STATE_FILE:-$STATE_DIR/state.env}
LOG_DIR=${LOG_DIR:-/var/log/sub2api-autodeploy}
KEEP_IMAGES=${KEEP_IMAGES:-4}

TARGET_COMMIT=
APP_CANDIDATE=
WORKER_CANDIDATE=

log() {
  printf '%s [%s] %s\n' "$(date --iso-8601=seconds)" "$PROGRAM" "$*"
}

die() {
  log "ERROR: $*" >&2
  exit 1
}

is_true() {
  case "${1,,}" in
    1|true|yes|on) return 0 ;;
    *) return 1 ;;
  esac
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

state_get() {
  local key=$1
  [[ -f "$STATE_FILE" ]] || return 0
  awk -F= -v key="$key" '$1 == key { value = substr($0, length(key) + 2) } END { print value }' "$STATE_FILE"
}

image_revision() {
  local image=${1:-}
  [[ -n "$image" ]] || return 0
  docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image" 2>/dev/null || true
}

container_image() {
  docker inspect --format '{{.Config.Image}}' "$1" 2>/dev/null || true
}

container_running() {
  [[ "$(docker inspect --format '{{.State.Running}}' "$1" 2>/dev/null || true)" == true ]]
}

app_healthy_now() {
  local docker_health
  docker_health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$APP_CONTAINER" 2>/dev/null || true)
  [[ "$docker_health" == healthy ]] && curl -fsS --max-time 8 "$HEALTH_URL" >/dev/null
}

wait_for_app() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
  local status health

  while (( SECONDS < deadline )); do
    status=$(docker inspect --format '{{.State.Status}}' "$APP_CONTAINER" 2>/dev/null || true)
    health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$APP_CONTAINER" 2>/dev/null || true)
    if [[ "$status" == running && "$health" == healthy ]] && curl -fsS --max-time 8 "$HEALTH_URL" >/dev/null 2>&1; then
      return 0
    fi
    if [[ "$status" == exited || "$status" == dead ]]; then
      log "application container entered state: $status"
      return 1
    fi
    sleep 5
  done

  log "application did not become healthy within ${HEALTH_TIMEOUT_SECONDS}s"
  return 1
}

wait_for_worker() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
  local stable_since=0
  local status restarting

  while (( SECONDS < deadline )); do
    status=$(docker inspect --format '{{.State.Status}}' "$WORKER_CONTAINER" 2>/dev/null || true)
    restarting=$(docker inspect --format '{{.State.Restarting}}' "$WORKER_CONTAINER" 2>/dev/null || true)
    if [[ "$status" == running && "$restarting" == false ]]; then
      if (( stable_since == 0 )); then
        stable_since=$SECONDS
      elif (( SECONDS - stable_since >= WORKER_STABLE_SECONDS )); then
        return 0
      fi
    else
      stable_since=0
    fi
    sleep 3
  done

  log "worker did not stay running for ${WORKER_STABLE_SECONDS}s"
  return 1
}

write_state() {
  local deployed_commit=$1
  local deployed_image=$2
  local deployed_worker_image=$3
  local previous_commit=$4
  local previous_image=$5
  local previous_worker_image=$6
  local tmp

  mkdir -p "$STATE_DIR"
  chmod 700 "$STATE_DIR"
  tmp=$(mktemp "$STATE_DIR/.state.XXXXXX")
  cat >"$tmp" <<EOF
DEPLOYED_COMMIT=$deployed_commit
DEPLOYED_IMAGE=$deployed_image
DEPLOYED_WORKER_IMAGE=$deployed_worker_image
PREVIOUS_COMMIT=$previous_commit
PREVIOUS_IMAGE=$previous_image
PREVIOUS_WORKER_IMAGE=$previous_worker_image
DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$STATE_FILE"
}

update_compose_env() {
  local app_image=$1
  local worker_image=$2
  local env_file=$DEPLOY_DIR/.env
  local tmp

  [[ -f "$env_file" ]] || die "environment file not found: $env_file"
  tmp=$(mktemp "$DEPLOY_DIR/.env.autodeploy.XXXXXX")
  awk -v app="$app_image" -v worker="$worker_image" '
    BEGIN { app_seen = 0; worker_seen = 0 }
    /^SUB2API_IMAGE=/ {
      if (!app_seen) print "SUB2API_IMAGE=" app
      app_seen = 1
      next
    }
    /^PRODUCT_SYNC_WORKER_IMAGE=/ {
      if (!worker_seen) print "PRODUCT_SYNC_WORKER_IMAGE=" worker
      worker_seen = 1
      next
    }
    { print }
    END {
      if (!app_seen) print "SUB2API_IMAGE=" app
      if (!worker_seen) print "PRODUCT_SYNC_WORKER_IMAGE=" worker
    }
  ' "$env_file" >"$tmp"
  chmod --reference="$env_file" "$tmp"
  chown --reference="$env_file" "$tmp"
  mv -f "$tmp" "$env_file"
}

show_status() {
  local deployed target source current_app current_worker
  deployed=$(state_get DEPLOYED_COMMIT)
  target=$(git -C "$REPO_DIR" rev-parse --verify "refs/remotes/$REMOTE/$DEPLOY_BRANCH^{commit}" 2>/dev/null || true)
  source=$(git -C "$REPO_DIR" rev-parse --verify "refs/remotes/$REMOTE/$SOURCE_BRANCH^{commit}" 2>/dev/null || true)
  current_app=$(container_image "$APP_CONTAINER")
  current_worker=$(container_image "$WORKER_CONTAINER")
  printf 'source_commit=%s\n' "$source"
  printf 'approved_commit=%s\n' "$target"
  printf 'deployed_commit=%s\n' "$deployed"
  printf 'app_image=%s\n' "$current_app"
  printf 'worker_image=%s\n' "$current_worker"
  printf 'app_healthy=%s\n' "$(app_healthy_now && echo yes || echo no)"
  printf 'worker_running=%s\n' "$(container_running "$WORKER_CONTAINER" && echo yes || echo no)"
}

refresh_approved_commit() {
  local deploy_ref="refs/remotes/$REMOTE/$DEPLOY_BRANCH"
  local source_ref="refs/remotes/$REMOTE/$SOURCE_BRANCH"

  log "fetching $REMOTE/$SOURCE_BRANCH and CI-approved $REMOTE/$DEPLOY_BRANCH"
  git -C "$REPO_DIR" fetch --prune "$REMOTE"

  git -C "$REPO_DIR" rev-parse --verify "$source_ref^{commit}" >/dev/null 2>&1 \
    || die "source branch not found: $REMOTE/$SOURCE_BRANCH"

  if ! git -C "$REPO_DIR" rev-parse --verify "$deploy_ref^{commit}" >/dev/null 2>&1; then
    log "no CI-approved deployment ref yet: $REMOTE/$DEPLOY_BRANCH"
    return 2
  fi

  TARGET_COMMIT=$(git -C "$REPO_DIR" rev-parse "$deploy_ref^{commit}")
  git -C "$REPO_DIR" merge-base --is-ancestor "$TARGET_COMMIT" "$source_ref" \
    || die "approved commit is not contained in $REMOTE/$SOURCE_BRANCH"
}

prepare_release_worktree() {
  if [[ -e "$BUILD_DIR" ]]; then
    git -C "$BUILD_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
      || die "build path exists but is not a Git worktree: $BUILD_DIR"
    [[ -z "$(git -C "$BUILD_DIR" status --porcelain)" ]] \
      || die "release worktree contains local changes: $BUILD_DIR"
    git -C "$BUILD_DIR" checkout --detach "$TARGET_COMMIT"
  else
    git -C "$REPO_DIR" worktree add --detach "$BUILD_DIR" "$TARGET_COMMIT"
  fi

  [[ "$(git -C "$BUILD_DIR" rev-parse HEAD)" == "$TARGET_COMMIT" ]] \
    || die "release worktree is not at approved commit"
}

run_docker_build() {
  local label=$1
  local logfile=$2
  shift 2
  local rc

  log "building $label; detailed log: $logfile"
  set +e
  DOCKER_BUILDKIT=1 docker build --progress=plain "$@" >"$logfile" 2>&1
  rc=$?
  set -e
  if (( rc != 0 )); then
    log "$label build failed (exit $rc); last 160 lines follow"
    tail -160 "$logfile" >&2 || true
    return "$rc"
  fi
}

build_images() {
  local short=${TARGET_COMMIT:0:12}
  local date_value
  local app_revision worker_revision
  date_value=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  APP_CANDIDATE="$APP_IMAGE_REPOSITORY:git-$TARGET_COMMIT"
  WORKER_CANDIDATE="$WORKER_IMAGE_REPOSITORY:git-$TARGET_COMMIT"
  mkdir -p "$LOG_DIR"
  chmod 750 "$LOG_DIR"

  if docker image inspect "$APP_CANDIDATE" >/dev/null 2>&1; then
    app_revision=$(image_revision "$APP_CANDIDATE")
    [[ "$app_revision" == "$TARGET_COMMIT" ]] \
      || die "existing app image has unexpected revision: $APP_CANDIDATE"
    log "reusing app image $APP_CANDIDATE"
  else
    run_docker_build "application image" "$LOG_DIR/app-$short.log" \
      --label "org.opencontainers.image.revision=$TARGET_COMMIT" \
      --label "org.opencontainers.image.source=https://github.com/Cloudsflee/sub2api" \
      --build-arg "VERSION=custom-$short" \
      --build-arg "COMMIT=$TARGET_COMMIT" \
      --build-arg "DATE=$date_value" \
      -t "$APP_CANDIDATE" "$BUILD_DIR"
  fi

  if is_true "$MANAGE_WORKER"; then
    [[ -f "$BUILD_DIR/deploy/product-sync-worker/Dockerfile" ]] \
      || die "worker Dockerfile not found"
    if docker image inspect "$WORKER_CANDIDATE" >/dev/null 2>&1; then
      worker_revision=$(image_revision "$WORKER_CANDIDATE")
      [[ "$worker_revision" == "$TARGET_COMMIT" ]] \
        || die "existing worker image has unexpected revision: $WORKER_CANDIDATE"
      log "reusing worker image $WORKER_CANDIDATE"
    else
      run_docker_build "worker image" "$LOG_DIR/worker-$short.log" \
        --label "org.opencontainers.image.revision=$TARGET_COMMIT" \
        --label "org.opencontainers.image.source=https://github.com/Cloudsflee/sub2api" \
        -t "$WORKER_CANDIDATE" "$BUILD_DIR/deploy/product-sync-worker"
    fi
  else
    WORKER_CANDIDATE=$(container_image "$WORKER_CONTAINER")
  fi
}

compose_up_service() {
  local service=$1
  local app_image=$2
  local worker_image=$3
  (
    cd "$DEPLOY_DIR"
    SUB2API_IMAGE="$app_image" \
    PRODUCT_SYNC_WORKER_IMAGE="$worker_image" \
      docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$service"
  )
}

rollback_after_failure() {
  local previous_app=$1
  local previous_worker=$2
  log "rolling back application to $previous_app"
  compose_up_service "$APP_SERVICE" "$previous_app" "$previous_worker" || true
  if ! wait_for_app; then
    log "CRITICAL: application rollback did not become healthy"
    return 1
  fi

  if is_true "$MANAGE_WORKER" && [[ -n "$previous_worker" ]]; then
    log "rolling back worker to $previous_worker"
    compose_up_service "$WORKER_SERVICE" "$previous_app" "$previous_worker" || true
    wait_for_worker || log "WARNING: worker rollback did not stabilize"
  fi
}

prune_old_images() {
  local repository=$1
  local active=$2
  local previous=$3
  local kept=0 image

  while IFS= read -r image; do
    [[ "$image" == "$repository:git-"* ]] || continue
    if [[ "$image" == "$active" || "$image" == "$previous" ]]; then
      continue
    fi
    if (( kept < KEEP_IMAGES )); then
      ((kept += 1))
      continue
    fi
    docker image rm "$image" >/dev/null 2>&1 || true
  done < <(docker image ls "$repository" --format '{{.Repository}}:{{.Tag}}')
}

deploy_approved_commit() {
  local deployed_commit deployed_image deployed_worker_image
  local previous_app previous_worker previous_commit

  deployed_commit=$(state_get DEPLOYED_COMMIT)
  deployed_image=$(state_get DEPLOYED_IMAGE)
  deployed_worker_image=$(state_get DEPLOYED_WORKER_IMAGE)
  if [[ "$MODE" != --force && "$deployed_commit" == "$TARGET_COMMIT" ]] \
    && [[ "$(container_image "$APP_CONTAINER")" == "$deployed_image" ]] \
    && app_healthy_now \
    && { ! is_true "$MANAGE_WORKER" \
      || { [[ "$(container_image "$WORKER_CONTAINER")" == "$deployed_worker_image" ]] \
        && container_running "$WORKER_CONTAINER"; }; }; then
    log "approved commit is already deployed and healthy: $TARGET_COMMIT"
    return 0
  fi

  prepare_release_worktree
  build_images

  previous_app=$(container_image "$APP_CONTAINER")
  previous_worker=$(container_image "$WORKER_CONTAINER")
  previous_commit=$deployed_commit
  [[ -n "$previous_app" ]] || die "cannot determine current application image"
  if is_true "$MANAGE_WORKER"; then
    [[ -n "$previous_worker" ]] || die "cannot determine current worker image"
  fi

  if is_true "$BACKUP_BEFORE_DEPLOY"; then
    [[ -x "$BACKUP_COMMAND" ]] || die "backup command is not executable: $BACKUP_COMMAND"
    log "creating pre-deployment backup"
    "$BACKUP_COMMAND"
  fi

  log "deploying application $APP_CANDIDATE"
  if ! compose_up_service "$APP_SERVICE" "$APP_CANDIDATE" "$WORKER_CANDIDATE" || ! wait_for_app; then
    log "candidate application failed health verification"
    rollback_after_failure "$previous_app" "$previous_worker" || true
    die "deployment failed and application rollback was attempted"
  fi

  if is_true "$MANAGE_WORKER"; then
    log "deploying worker $WORKER_CANDIDATE"
    if ! compose_up_service "$WORKER_SERVICE" "$APP_CANDIDATE" "$WORKER_CANDIDATE" || ! wait_for_worker; then
      log "candidate worker failed stability verification"
      rollback_after_failure "$previous_app" "$previous_worker" || true
      die "deployment failed and rollback was attempted"
    fi
  fi

  update_compose_env "$APP_CANDIDATE" "$WORKER_CANDIDATE"
  write_state "$TARGET_COMMIT" "$APP_CANDIDATE" "$WORKER_CANDIDATE" \
    "$previous_commit" "$previous_app" "$previous_worker"
  docker tag "$APP_CANDIDATE" "$APP_IMAGE_REPOSITORY:stable"
  if is_true "$MANAGE_WORKER"; then
    docker tag "$WORKER_CANDIDATE" "$WORKER_IMAGE_REPOSITORY:stable"
  fi

  prune_old_images "$APP_IMAGE_REPOSITORY" "$APP_CANDIDATE" "$previous_app"
  if is_true "$MANAGE_WORKER"; then
    prune_old_images "$WORKER_IMAGE_REPOSITORY" "$WORKER_CANDIDATE" "$previous_worker"
  fi
  find "$LOG_DIR" -type f -name '*.log' -mtime +14 -delete 2>/dev/null || true
  log "deployment succeeded: $TARGET_COMMIT"
}

manual_rollback() {
  local current_commit current_app current_worker rollback_commit rollback_app rollback_worker
  current_commit=$(state_get DEPLOYED_COMMIT)
  rollback_commit=$(state_get PREVIOUS_COMMIT)
  rollback_app=$(state_get PREVIOUS_IMAGE)
  rollback_worker=$(state_get PREVIOUS_WORKER_IMAGE)
  current_app=$(container_image "$APP_CONTAINER")
  current_worker=$(container_image "$WORKER_CONTAINER")

  [[ -n "$rollback_app" ]] || die "no previous application image recorded"
  if is_true "$BACKUP_BEFORE_DEPLOY" && [[ -x "$BACKUP_COMMAND" ]]; then
    log "creating pre-rollback backup"
    "$BACKUP_COMMAND"
  fi

  compose_up_service "$APP_SERVICE" "$rollback_app" "$rollback_worker"
  wait_for_app || die "rolled-back application did not become healthy"
  if is_true "$MANAGE_WORKER" && [[ -n "$rollback_worker" ]]; then
    compose_up_service "$WORKER_SERVICE" "$rollback_app" "$rollback_worker"
    wait_for_worker || die "rolled-back worker did not stabilize"
  fi

  update_compose_env "$rollback_app" "$rollback_worker"
  write_state "$rollback_commit" "$rollback_app" "$rollback_worker" \
    "$current_commit" "$current_app" "$current_worker"
  log "rollback succeeded: ${rollback_commit:-unknown commit}"
}

case "$MODE" in
  --deploy|--force|--check|--build-only|--status|--rollback) ;;
  *) die "usage: $PROGRAM [--deploy|--force|--check|--build-only|--status|--rollback]" ;;
esac

for command_name in git docker curl flock awk; do
  require_command "$command_name"
done

mkdir -p "$(dirname "$LOCK_FILE")" "$STATE_DIR" "$LOG_DIR"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  log "another deployment or health recovery operation is active; skipping"
  exit 0
fi

[[ -d "$REPO_DIR" ]] || die "repository directory not found: $REPO_DIR"
[[ -f "$COMPOSE_FILE" ]] || die "Compose file not found: $COMPOSE_FILE"
# These are literal Compose interpolation markers, not shell expressions.
# shellcheck disable=SC2016
app_image_marker='${SUB2API_IMAGE'
# shellcheck disable=SC2016
worker_image_marker='${PRODUCT_SYNC_WORKER_IMAGE'
grep -Fq "$app_image_marker" "$COMPOSE_FILE" \
  || die "Compose file is not configured with SUB2API_IMAGE; run the installer"
if is_true "$MANAGE_WORKER"; then
  grep -Fq "$worker_image_marker" "$COMPOSE_FILE" \
    || die "Compose file is not configured with PRODUCT_SYNC_WORKER_IMAGE; run the installer"
fi

if [[ "$MODE" == --status ]]; then
  show_status
  exit 0
fi

if [[ "$MODE" == --rollback ]]; then
  manual_rollback
  exit 0
fi

if refresh_approved_commit; then
  :
else
  rc=$?
  (( rc == 2 )) && exit 0
  exit "$rc"
fi

if [[ "$MODE" == --check ]]; then
  show_status
  if [[ "$(state_get DEPLOYED_COMMIT)" == "$TARGET_COMMIT" ]]; then
    log "no deployment pending"
  else
    log "deployment pending for $TARGET_COMMIT"
  fi
  exit 0
fi

if [[ "$MODE" == --build-only ]]; then
  prepare_release_worktree
  build_images
  log "CI-approved images are ready: $APP_CANDIDATE and $WORKER_CANDIDATE"
  exit 0
fi

deploy_approved_commit
