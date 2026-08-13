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
BLUE_SERVICE=${BLUE_SERVICE:-sub2api-blue}
GREEN_SERVICE=${GREEN_SERVICE:-sub2api-green}
BLUE_CONTAINER=${BLUE_CONTAINER:-sub2api-blue}
GREEN_CONTAINER=${GREEN_CONTAINER:-sub2api-green}
BLUE_PORT=${BLUE_PORT:-18080}
GREEN_PORT=${GREEN_PORT:-18081}
WORKER_SERVICE=${WORKER_SERVICE:-product-sync-worker}
WORKER_CONTAINER=${WORKER_CONTAINER:-sub2api-product-sync-worker}
APP_IMAGE_REPOSITORY=${APP_IMAGE_REPOSITORY:-sub2api-custom}
WORKER_IMAGE_REPOSITORY=${WORKER_IMAGE_REPOSITORY:-sub2api-product-sync-worker}
PROXY_HEALTH_URL=${PROXY_HEALTH_URL:-${HEALTH_URL:-http://127.0.0.1:8080/health}}
HEALTH_TIMEOUT_SECONDS=${HEALTH_TIMEOUT_SECONDS:-180}
HEALTH_CONSECUTIVE_SUCCESSES=${HEALTH_CONSECUTIVE_SUCCESSES:-3}
HEALTH_CHECK_INTERVAL_SECONDS=${HEALTH_CHECK_INTERVAL_SECONDS:-2}
STABILITY_SECONDS=${STABILITY_SECONDS:-30}
WORKER_STABLE_SECONDS=${WORKER_STABLE_SECONDS:-15}
DRAIN_TIMEOUT_SECONDS=${DRAIN_TIMEOUT_SECONDS:-600}
DRAIN_POLL_SECONDS=${DRAIN_POLL_SECONDS:-2}
BACKUP_BEFORE_DEPLOY=${BACKUP_BEFORE_DEPLOY:-true}
BACKUP_COMMAND=${BACKUP_COMMAND:-/usr/local/sbin/sub2api-backup.sh}
MANAGE_WORKER=${MANAGE_WORKER:-true}
HAPROXY_CONFIG_COMMAND=${HAPROXY_CONFIG_COMMAND:-/usr/local/sbin/sub2api-haproxy-config}
HAPROXY_SERVICE=${HAPROXY_SERVICE:-haproxy}
LOCK_FILE=${LOCK_FILE:-/run/lock/sub2api-deploy.lock}
STATE_DIR=${STATE_DIR:-/var/lib/sub2api-autodeploy}
STATE_FILE=${STATE_FILE:-$STATE_DIR/state.env}
LOG_DIR=${LOG_DIR:-/var/log/sub2api-autodeploy}
MIGRATIONS_PATH=${MIGRATIONS_PATH:-backend/migrations}
KEEP_IMAGES=${KEEP_IMAGES:-4}
PRUNE_BUILD_CACHE=${PRUNE_BUILD_CACHE:-true}
BUILD_CACHE_MAX_USED_SPACE=${BUILD_CACHE_MAX_USED_SPACE:-6gb}
BUILD_CACHE_MIN_FREE_SPACE=${BUILD_CACHE_MIN_FREE_SPACE:-8gb}

TARGET_COMMIT=
APP_CANDIDATE=
WORKER_CANDIDATE=
WORKER_BUILD_PATHS=(
  deploy/product-sync-worker/.dockerignore
  deploy/product-sync-worker/Dockerfile
  deploy/product-sync-worker/catalog-sync.js
  deploy/product-sync-worker/challenge-manager.js
  deploy/product-sync-worker/healthcheck.js
  deploy/product-sync-worker/index.js
  deploy/product-sync-worker/package-lock.json
  deploy/product-sync-worker/package.json
  deploy/product-sync-worker/start-worker.sh
  deploy/product-sync-worker/worker-utils.js
)

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

container_health() {
  docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$1" 2>/dev/null || true
}

valid_slot() {
  [[ "$1" == blue || "$1" == green ]]
}

other_slot() {
  if [[ "$1" == blue ]]; then
    printf 'green\n'
  else
    printf 'blue\n'
  fi
}

slot_service() {
  if [[ "$1" == blue ]]; then
    printf '%s\n' "$BLUE_SERVICE"
  else
    printf '%s\n' "$GREEN_SERVICE"
  fi
}

slot_container() {
  if [[ "$1" == blue ]]; then
    printf '%s\n' "$BLUE_CONTAINER"
  else
    printf '%s\n' "$GREEN_CONTAINER"
  fi
}

slot_port() {
  if [[ "$1" == blue ]]; then
    printf '%s\n' "$BLUE_PORT"
  else
    printf '%s\n' "$GREEN_PORT"
  fi
}

slot_health_url() {
  printf 'http://127.0.0.1:%s/health\n' "$(slot_port "$1")"
}

slot_state_image() {
  local key
  if [[ "$1" == blue ]]; then
    key=BLUE_IMAGE
  else
    key=GREEN_IMAGE
  fi
  state_get "$key"
}

resolve_active_slot() {
  local configured= recorded= blue_running=false green_running=false

  if [[ -x "$HAPROXY_CONFIG_COMMAND" ]]; then
    configured=$("$HAPROXY_CONFIG_COMMAND" --current 2>/dev/null || true)
  fi
  recorded=$(state_get ACTIVE_SLOT)
  if valid_slot "$configured"; then
    if valid_slot "$recorded" && [[ "$configured" != "$recorded" ]]; then
      log "WARNING: HAProxy active slot $configured differs from state $recorded; using HAProxy" >&2
    fi
    printf '%s\n' "$configured"
    return 0
  fi
  if valid_slot "$recorded"; then
    printf '%s\n' "$recorded"
    return 0
  fi

  container_running "$BLUE_CONTAINER" && blue_running=true
  container_running "$GREEN_CONTAINER" && green_running=true
  if [[ "$blue_running" == true && "$green_running" == false ]]; then
    printf 'blue\n'
    return 0
  fi
  if [[ "$green_running" == true && "$blue_running" == false ]]; then
    printf 'green\n'
    return 0
  fi
  return 1
}

require_active_slot() {
  local slot
  slot=$(resolve_active_slot) || die "cannot determine active blue/green slot"
  valid_slot "$slot" || die "invalid active slot: $slot"
  printf '%s\n' "$slot"
}

slot_healthy_now() {
  local slot=$1 container url
  container=$(slot_container "$slot")
  url=$(slot_health_url "$slot")
  container_running "$container" \
    && [[ "$(container_health "$container")" == healthy ]] \
    && curl -fsS --max-time 8 "$url" >/dev/null
}

proxy_healthy_now() {
  systemctl is-active --quiet "$HAPROXY_SERVICE" \
    && curl -fsS --max-time 8 "$PROXY_HEALTH_URL" >/dev/null
}

wait_for_slot() {
  local slot=$1 container url deadline status health successes=0
  container=$(slot_container "$slot")
  url=$(slot_health_url "$slot")
  deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))

  while (( SECONDS < deadline )); do
    status=$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null || true)
    health=$(container_health "$container")
    if [[ "$status" == running && "$health" == healthy ]] \
      && curl -fsS --max-time 8 "$url" >/dev/null 2>&1; then
      ((successes += 1))
      if (( successes >= HEALTH_CONSECUTIVE_SUCCESSES )); then
        return 0
      fi
    else
      successes=0
    fi
    if [[ "$status" == exited || "$status" == dead || "$status" == restarting ]]; then
      log "slot $slot container entered state: $status"
      return 1
    fi
    sleep "$HEALTH_CHECK_INTERVAL_SECONDS"
  done

  log "slot $slot did not pass ${HEALTH_CONSECUTIVE_SUCCESSES} consecutive health checks within ${HEALTH_TIMEOUT_SECONDS}s"
  return 1
}

wait_for_worker() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
  local stable_since=0 status restarting

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
  local deployed_commit=$1 deployed_image=$2 deployed_worker_image=$3
  local previous_commit=$4 previous_image=$5 previous_worker_image=$6
  local active_slot=$7 blue_image=$8 green_image=$9 previous_slot=${10}
  local tmp

  mkdir -p "$STATE_DIR"
  chmod 700 "$STATE_DIR"
  tmp=$(mktemp "$STATE_DIR/.state.XXXXXX")
  cat >"$tmp" <<EOF
DEPLOYED_COMMIT=$deployed_commit
DEPLOYED_IMAGE=$deployed_image
DEPLOYED_WORKER_IMAGE=$deployed_worker_image
ACTIVE_SLOT=$active_slot
BLUE_IMAGE=$blue_image
GREEN_IMAGE=$green_image
PREVIOUS_COMMIT=$previous_commit
PREVIOUS_IMAGE=$previous_image
PREVIOUS_WORKER_IMAGE=$previous_worker_image
PREVIOUS_SLOT=$previous_slot
DEPLOYED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  chmod 600 "$tmp"
  mv -f "$tmp" "$STATE_FILE"
}

update_compose_env() {
  local app_image=$1 worker_image=$2 env_file=$DEPLOY_DIR/.env tmp

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
  local deployed target source active blue_image green_image current_worker
  deployed=$(state_get DEPLOYED_COMMIT)
  target=$(git -C "$REPO_DIR" rev-parse --verify "refs/remotes/$REMOTE/$DEPLOY_BRANCH^{commit}" 2>/dev/null || true)
  source=$(git -C "$REPO_DIR" rev-parse --verify "refs/remotes/$REMOTE/$SOURCE_BRANCH^{commit}" 2>/dev/null || true)
  active=$(resolve_active_slot || true)
  blue_image=$(container_image "$BLUE_CONTAINER")
  green_image=$(container_image "$GREEN_CONTAINER")
  current_worker=$(container_image "$WORKER_CONTAINER")
  printf 'source_commit=%s\n' "$source"
  printf 'approved_commit=%s\n' "$target"
  printf 'deployed_commit=%s\n' "$deployed"
  printf 'active_slot=%s\n' "$active"
  printf 'blue_image=%s\n' "$blue_image"
  printf 'blue_running=%s\n' "$(container_running "$BLUE_CONTAINER" && echo yes || echo no)"
  printf 'blue_healthy=%s\n' "$(slot_healthy_now blue && echo yes || echo no)"
  printf 'green_image=%s\n' "$green_image"
  printf 'green_running=%s\n' "$(container_running "$GREEN_CONTAINER" && echo yes || echo no)"
  printf 'green_healthy=%s\n' "$(slot_healthy_now green && echo yes || echo no)"
  printf 'proxy_healthy=%s\n' "$(proxy_healthy_now && echo yes || echo no)"
  printf 'worker_image=%s\n' "$current_worker"
  printf 'worker_running=%s\n' "$(container_running "$WORKER_CONTAINER" && echo yes || echo no)"
}

refresh_approved_commit() {
  local deploy_ref="refs/remotes/$REMOTE/$DEPLOY_BRANCH"
  local source_ref="refs/remotes/$REMOTE/$SOURCE_BRANCH"

  log "fetching $REMOTE/$SOURCE_BRANCH and CI-approved $REMOTE/$DEPLOY_BRANCH"
  git -C "$REPO_DIR" fetch --prune --tags "$REMOTE"
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

check_migration_compatibility() {
  local baseline=$1 target=$2 status path previous_path normalized remaining failed=0

  if [[ -z "$baseline" ]]; then
    log "migration gate cannot determine the deployed commit"
    return 1
  fi
  if ! git -C "$REPO_DIR" cat-file -e "$baseline^{commit}" 2>/dev/null; then
    log "migration gate cannot find deployed commit: $baseline"
    return 1
  fi
  if ! git -C "$REPO_DIR" merge-base --is-ancestor "$baseline" "$target"; then
    log "migration gate requires a forward deployment from $baseline to $target"
    return 1
  fi

  while IFS=$'\t' read -r status path previous_path; do
    [[ -n "$status" ]] || continue
    if [[ "$status" != A ]]; then
      log "migration gate rejected modified existing migration: $status $path${previous_path:+ -> $previous_path}"
      failed=1
      continue
    fi

    # Remove comments and quoted literals before looking for destructive
    # statements.  A migration may legitimately mention DROP/RENAME in a
    # COMMENT or rollback hint; those words are not executable SQL.
    normalized=$(git -C "$REPO_DIR" show "$target:$path" \
      | sed -E "s/'([^']|'')*'//g; s/\"([^\"]|\"\")*\"//g; s/--.*$//" \
      | tr '[:upper:]' '[:lower:]' \
      | tr '\n' ' ')

    # Adding a NOT NULL column to an existing table is only safe when the
    # column has a DEFAULT in the same ADD COLUMN clause.  Remove those safe
    # clauses first so a second unsafe ADD COLUMN in a multi-column ALTER is
    # still rejected.  This permits official additive migrations such as
    # `ADD COLUMN ... NOT NULL DEFAULT ...` while retaining the old guard.
    remaining=$(printf '%s' "$normalized" | sed -E \
      's/add[[:space:]]+column[^,;]*not[[:space:]]+null[^,;]*default[^,;]*(,|;|$)/ /g')
    if grep -Eiq \
      '(^|[^[:alnum:]_])(DROP|RENAME)([^[:alnum:]_]|$)|ALTER[[:space:]]+COLUMN[^;]*(TYPE|SET[[:space:]]+DATA[[:space:]]+TYPE)|ALTER[[:space:]]+TABLE[^;]*(SET[[:space:]]+NOT[[:space:]]+NULL|ADD[[:space:]]+COLUMN[^,;]*NOT[[:space:]]+NULL)' \
      <<<"$remaining"; then
      log "migration gate rejected destructive SQL in new migration: $path"
      failed=1
    else
      log "migration gate accepted additive migration: $path"
    fi
  done < <(git -C "$REPO_DIR" diff --name-status --find-renames \
    "$baseline" "$target" -- "$MIGRATIONS_PATH")

  (( failed == 0 ))
}

worker_build_inputs_changed() {
  local baseline=${1:-} target=${2:-}
  [[ -n "$baseline" && -n "$target" ]] || return 0
  git -C "$REPO_DIR" cat-file -e "$baseline^{commit}" 2>/dev/null || return 0
  git -C "$REPO_DIR" cat-file -e "$target^{commit}" 2>/dev/null || return 0
  ! git -C "$REPO_DIR" diff --quiet "$baseline" "$target" -- "${WORKER_BUILD_PATHS[@]}"
}

prune_build_cache() {
  local output rc summary
  is_true "$PRUNE_BUILD_CACHE" || return 0
  log "pruning unused Docker build cache (max ${BUILD_CACHE_MAX_USED_SPACE}, min free ${BUILD_CACHE_MIN_FREE_SPACE})"
  set +e
  output=$(docker buildx prune --all --force \
    --filter 'type!=exec.cachemount' \
    --max-used-space "$BUILD_CACHE_MAX_USED_SPACE" \
    --min-free-space "$BUILD_CACHE_MIN_FREE_SPACE" 2>&1)
  rc=$?
  set -e
  if (( rc != 0 )); then
    log "WARNING: Docker build cache prune failed (exit $rc)"
    [[ -z "$output" ]] || printf '%s\n' "$output" >&2
    return 0
  fi
  summary=${output##*$'\n'}
  log "Docker build cache prune completed: ${summary:-no cache removed}"
}

run_docker_build() {
  local label=$1 logfile=$2 rc
  shift 2
  log "building $label; detailed log: $logfile"
  set +e
  DOCKER_BUILDKIT=1 docker build --progress=plain "$@" >"$logfile" 2>&1
  rc=$?
  set -e
  if (( rc != 0 )); then
    log "$label build failed (exit $rc); last 160 lines follow"
    tail -160 "$logfile" >&2 || true
    prune_build_cache
    return "$rc"
  fi
}

resolve_reachable_tag_version() {
  local prefix=$1 pattern=$2 ref version
  while IFS= read -r ref; do
    version=${ref#"$prefix"}
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
    if git -C "$BUILD_DIR" merge-base --is-ancestor "$ref^{commit}" "$TARGET_COMMIT"; then
      printf '%s\n' "$version"
      return 0
    fi
  done < <(git -C "$BUILD_DIR" for-each-ref \
    --sort=-version:refname --format='%(refname:short)' "$pattern")
  return 1
}

version_is_at_least() {
  local left=$1 right=$2 maximum
  maximum=$(printf '%s\n%s\n' "$left" "$right" | sort -V | tail -n 1)
  [[ "$maximum" == "$left" ]]
}

resolve_official_version() {
  local version='' candidate version_file upstream_version local_version
  version_file=$(tr -d '\r\n' <"$BUILD_DIR/backend/cmd/server/VERSION" 2>/dev/null || true)
  upstream_version=$(resolve_reachable_tag_version 'upstream/v' 'refs/tags/upstream/v*' || true)
  local_version=$(resolve_reachable_tag_version 'v' 'refs/tags/v*' || true)

  # A stale host-side tag must not override the newer VERSION synced into the
  # source tree. This matters when the repository fetch has not brought the
  # newest upstream release tag to the deployment host yet.
  for candidate in "$version_file" "$upstream_version" "$local_version"; do
    [[ "$candidate" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || continue
    if [[ -z "$version" ]] || version_is_at_least "$candidate" "$version"; then
      version=$candidate
    fi
  done

  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || die "cannot determine official base version for $TARGET_COMMIT"
  printf '%s\n' "$version"
}

build_images() {
  local short=${TARGET_COMMIT:0:12}
  local date_value official_version version_value app_revision worker_revision
  local baseline current_worker
  baseline=${1:-$(state_get DEPLOYED_COMMIT)}
  date_value=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  official_version=$(resolve_official_version)
  version_value="$official_version-custom.$short"

  APP_CANDIDATE="$APP_IMAGE_REPOSITORY:git-$TARGET_COMMIT"
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
      --build-arg "VERSION=$version_value" \
      --build-arg "COMMIT=$TARGET_COMMIT" \
      --build-arg "DATE=$date_value" \
      -t "$APP_CANDIDATE" "$BUILD_DIR"
  fi

  if is_true "$MANAGE_WORKER"; then
    [[ -f "$BUILD_DIR/deploy/product-sync-worker/Dockerfile" ]] \
      || die "worker Dockerfile not found"
    current_worker=$(container_image "$WORKER_CONTAINER")
    if [[ -n "$current_worker" ]] && ! worker_build_inputs_changed "$baseline" "$TARGET_COMMIT"; then
      WORKER_CANDIDATE=$current_worker
      log "worker build inputs are unchanged; reusing $WORKER_CANDIDATE"
    else
      WORKER_CANDIDATE="$WORKER_IMAGE_REPOSITORY:git-$TARGET_COMMIT"
    fi
    if [[ "$WORKER_CANDIDATE" == "$current_worker" ]]; then
      :
    elif docker image inspect "$WORKER_CANDIDATE" >/dev/null 2>&1; then
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

compose_up_slot() {
  local slot=$1 app_image=$2 worker_image=$3 service
  service=$(slot_service "$slot")
  (
    cd "$DEPLOY_DIR"
    SUB2API_IMAGE="$app_image" PRODUCT_SYNC_WORKER_IMAGE="$worker_image" \
      docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "$service"
  )
}

compose_stop_slot() {
  local service
  service=$(slot_service "$1")
  (
    cd "$DEPLOY_DIR"
    docker compose -f "$COMPOSE_FILE" stop --timeout 3 "$service"
  )
}

compose_up_worker() {
  local app_image=$1 worker_image=$2
  (
    cd "$DEPLOY_DIR"
    SUB2API_IMAGE="$app_image" PRODUCT_SYNC_WORKER_IMAGE="$worker_image" \
      docker compose -f "$COMPOSE_FILE" up -d --no-deps "$WORKER_SERVICE"
  )
}

capture_container_diagnostics() {
  local container=$1 label=$2 stamp destination
  stamp=$(date +%Y%m%d-%H%M%S)
  destination="$LOG_DIR/${label}-failure-$stamp.log"
  {
    echo "=== docker inspect ==="
    docker inspect "$container" 2>&1 || true
    echo "=== docker logs ==="
    docker logs --tail 300 "$container" 2>&1 || true
  } >"$destination"
  chmod 640 "$destination"
  log "captured failed container diagnostics: $destination"
  tail -80 "$destination" >&2 || true
}

validate_haproxy_slot() {
  "$HAPROXY_CONFIG_COMMAND" --validate "$1"
}

activate_haproxy_slot() {
  local slot=$1
  log "activating HAProxy slot $slot"
  "$HAPROXY_CONFIG_COMMAND" --activate "$slot"
}

observe_switched_stack() {
  local slot=$1 deadline=$((SECONDS + STABILITY_SECONDS))
  while (( SECONDS < deadline )); do
    if ! slot_healthy_now "$slot"; then
      log "slot $slot failed during the ${STABILITY_SECONDS}s observation window"
      return 1
    fi
    if ! proxy_healthy_now; then
      log "HAProxy failed during the ${STABILITY_SECONDS}s observation window"
      return 1
    fi
    if is_true "$MANAGE_WORKER" && ! container_running "$WORKER_CONTAINER"; then
      log "worker stopped during the ${STABILITY_SECONDS}s observation window"
      return 1
    fi
    sleep 2
  done
}

slot_connection_count() {
  local port
  port=$(slot_port "$1")
  ss -Hnt state established 2>/dev/null \
    | awk -v suffix=":$port" '
        $4 ~ (suffix "$") || $5 ~ (suffix "$") { count += 1 }
        END { print count + 0 }
      '
}

wait_for_slot_drain() {
  local draining_slot=$1 active_slot=$2 switched_at=$3
  local deadline=$((switched_at + DRAIN_TIMEOUT_SECONDS)) now connections zero_checks=0

  while :; do
    if ! slot_healthy_now "$active_slot" || ! proxy_healthy_now; then
      log "active stack failed while slot $draining_slot was draining"
      return 2
    fi
    if is_true "$MANAGE_WORKER" && ! container_running "$WORKER_CONTAINER"; then
      log "worker failed while slot $draining_slot was draining"
      return 2
    fi

    connections=$(slot_connection_count "$draining_slot" || true)
    [[ "$connections" =~ ^[0-9]+$ ]] || connections=999999
    if (( connections == 0 )); then
      ((zero_checks += 1))
      if (( zero_checks >= 3 )); then
        log "slot $draining_slot has no remaining backend connections"
        return 0
      fi
    else
      zero_checks=0
    fi

    now=$(date +%s)
    if (( now >= deadline )); then
      log "slot $draining_slot reached the ${DRAIN_TIMEOUT_SECONDS}s drain limit with $connections socket entries"
      return 0
    fi
    sleep "$DRAIN_POLL_SECONDS"
  done
}

run_backup() {
  local purpose=$1 rc
  [[ -x "$BACKUP_COMMAND" ]] || die "backup command is not executable: $BACKUP_COMMAND"
  log "creating $purpose backup"
  if "$BACKUP_COMMAND"; then
    return 0
  else
    rc=$?
    die "$purpose backup failed (exit $rc)"
  fi
}

restore_failed_switch() {
  local old_slot=$1 candidate_slot=$2 old_commit=$3 old_app=$4 old_worker=$5
  local original_previous_commit=$6 original_previous_image=$7
  local original_previous_worker=$8 original_previous_slot=$9 switched_at=${10}
  local blue_image green_image

  log "hot-switching traffic back to slot $old_slot"
  if ! activate_haproxy_slot "$old_slot"; then
    log "CRITICAL: HAProxy could not switch back to slot $old_slot"
    return 1
  fi
  if is_true "$MANAGE_WORKER" && [[ -n "$old_worker" ]]; then
    compose_up_worker "$old_app" "$old_worker" || true
    wait_for_worker || log "WARNING: worker rollback did not stabilize"
  fi
  wait_for_slot "$old_slot" || {
    log "CRITICAL: old slot $old_slot is not healthy after hot rollback"
    return 1
  }
  update_compose_env "$old_app" "$old_worker"
  blue_image=$(container_image "$BLUE_CONTAINER")
  green_image=$(container_image "$GREEN_CONTAINER")
  write_state "$old_commit" "$old_app" "$old_worker" \
    "$original_previous_commit" "$original_previous_image" "$original_previous_worker" \
    "$old_slot" "$blue_image" "$green_image" "$original_previous_slot"

  wait_for_slot_drain "$candidate_slot" "$old_slot" "$switched_at" || true
  compose_stop_slot "$candidate_slot" || true
  log "hot rollback restored slot $old_slot"
}

perform_hot_switch() {
  local target_commit=$1 target_app=$2 target_worker=$3
  local old_slot candidate_slot old_commit old_app old_worker
  local original_previous_commit original_previous_image original_previous_worker
  local original_previous_slot switched_at blue_image green_image drain_rc

  old_slot=$(require_active_slot)
  candidate_slot=$(other_slot "$old_slot")
  old_commit=$(state_get DEPLOYED_COMMIT)
  old_app=$(container_image "$(slot_container "$old_slot")")
  old_worker=$(container_image "$WORKER_CONTAINER")
  original_previous_commit=$(state_get PREVIOUS_COMMIT)
  original_previous_image=$(state_get PREVIOUS_IMAGE)
  original_previous_worker=$(state_get PREVIOUS_WORKER_IMAGE)
  original_previous_slot=$(state_get PREVIOUS_SLOT)

  [[ -n "$old_commit" ]] || {
    log "cannot determine currently deployed commit"
    return 1
  }
  [[ -n "$old_app" ]] || {
    log "cannot determine active slot image"
    return 1
  }
  if is_true "$MANAGE_WORKER" && [[ -z "$old_worker" ]]; then
    log "cannot determine current worker image"
    return 1
  fi
  if ! slot_healthy_now "$old_slot" || ! proxy_healthy_now; then
    log "active slot or HAProxy is not healthy before deployment"
    return 1
  fi

  log "starting candidate $candidate_slot with $target_app"
  if ! compose_up_slot "$candidate_slot" "$target_app" "$target_worker" \
    || ! wait_for_slot "$candidate_slot"; then
    log "candidate slot $candidate_slot failed health verification"
    capture_container_diagnostics "$(slot_container "$candidate_slot")" "application-$candidate_slot"
    compose_stop_slot "$candidate_slot" || true
    return 1
  fi

  # Candidate startup may have applied additive migrations to the shared DB.
  # The old binary must still be viable before any traffic moves.
  if ! slot_healthy_now "$old_slot"; then
    log "old slot $old_slot became unhealthy after candidate migrations"
    capture_container_diagnostics "$(slot_container "$candidate_slot")" "migration-compatibility"
    compose_stop_slot "$candidate_slot" || true
    return 1
  fi
  if ! validate_haproxy_slot "$candidate_slot"; then
    log "generated HAProxy configuration for $candidate_slot is invalid"
    compose_stop_slot "$candidate_slot" || true
    return 1
  fi

  switched_at=$(date +%s)
  if ! activate_haproxy_slot "$candidate_slot"; then
    compose_stop_slot "$candidate_slot" || true
    return 1
  fi

  if is_true "$MANAGE_WORKER"; then
    log "deploying worker $target_worker"
    if ! compose_up_worker "$target_app" "$target_worker" || ! wait_for_worker; then
      log "candidate worker failed stability verification"
      capture_container_diagnostics "$WORKER_CONTAINER" worker
      restore_failed_switch "$old_slot" "$candidate_slot" "$old_commit" "$old_app" "$old_worker" \
        "$original_previous_commit" "$original_previous_image" "$original_previous_worker" \
        "$original_previous_slot" "$switched_at" || true
      return 1
    fi
  fi

  if ! observe_switched_stack "$candidate_slot"; then
    restore_failed_switch "$old_slot" "$candidate_slot" "$old_commit" "$old_app" "$old_worker" \
      "$original_previous_commit" "$original_previous_image" "$original_previous_worker" \
      "$original_previous_slot" "$switched_at" || true
    return 1
  fi

  update_compose_env "$target_app" "$target_worker"
  blue_image=$(container_image "$BLUE_CONTAINER")
  green_image=$(container_image "$GREEN_CONTAINER")
  write_state "$target_commit" "$target_app" "$target_worker" \
    "$old_commit" "$old_app" "$old_worker" \
    "$candidate_slot" "$blue_image" "$green_image" "$old_slot"

  set +e
  wait_for_slot_drain "$old_slot" "$candidate_slot" "$switched_at"
  drain_rc=$?
  set -e
  if (( drain_rc == 2 )); then
    restore_failed_switch "$old_slot" "$candidate_slot" "$old_commit" "$old_app" "$old_worker" \
      "$original_previous_commit" "$original_previous_image" "$original_previous_worker" \
      "$original_previous_slot" "$switched_at" || true
    return 1
  fi
  compose_stop_slot "$old_slot" || {
    log "failed to stop drained slot $old_slot"
    return 1
  }
  log "slot $candidate_slot is active; slot $old_slot is stopped"
}

prune_old_images() {
  local repository=$1
  shift
  local kept=0 image protected item
  while IFS= read -r image; do
    [[ "$image" == "$repository:git-"* ]] || continue
    protected=false
    for item in "$@"; do
      if [[ -n "$item" && "$image" == "$item" ]]; then
        protected=true
        break
      fi
    done
    [[ "$protected" == true ]] && continue
    if (( kept < KEEP_IMAGES )); then
      ((kept += 1))
      continue
    fi
    docker image rm "$image" >/dev/null 2>&1 || true
  done < <(docker image ls "$repository" --format '{{.Repository}}:{{.Tag}}')
}

deploy_approved_commit() {
  local deployed_commit deployed_image deployed_worker_image active current_app
  local previous_app previous_worker blue_image green_image
  deployed_commit=$(state_get DEPLOYED_COMMIT)
  deployed_image=$(state_get DEPLOYED_IMAGE)
  deployed_worker_image=$(state_get DEPLOYED_WORKER_IMAGE)
  active=$(require_active_slot)
  current_app=$(container_image "$(slot_container "$active")")

  if [[ "$MODE" != --force && "$deployed_commit" == "$TARGET_COMMIT" ]] \
    && [[ "$current_app" == "$deployed_image" ]] \
    && slot_healthy_now "$active" && proxy_healthy_now \
    && { ! is_true "$MANAGE_WORKER" \
      || { [[ "$(container_image "$WORKER_CONTAINER")" == "$deployed_worker_image" ]] \
        && container_running "$WORKER_CONTAINER"; }; }; then
    log "approved commit is already deployed and healthy: $TARGET_COMMIT"
    return 0
  fi

  prepare_release_worktree
  check_migration_compatibility "$deployed_commit" "$TARGET_COMMIT" \
    || die "automatic deployment stopped by migration compatibility gate"
  build_images
  prune_build_cache

  previous_app=$current_app
  previous_worker=$(container_image "$WORKER_CONTAINER")
  if is_true "$BACKUP_BEFORE_DEPLOY"; then
    run_backup "pre-deployment"
  fi

  perform_hot_switch "$TARGET_COMMIT" "$APP_CANDIDATE" "$WORKER_CANDIDATE" \
    || die "deployment failed; traffic rollback was attempted when required"

  docker tag "$APP_CANDIDATE" "$APP_IMAGE_REPOSITORY:stable"
  if is_true "$MANAGE_WORKER"; then
    docker tag "$WORKER_CANDIDATE" "$WORKER_IMAGE_REPOSITORY:stable"
  fi

  blue_image=$(container_image "$BLUE_CONTAINER")
  green_image=$(container_image "$GREEN_CONTAINER")
  prune_old_images "$APP_IMAGE_REPOSITORY" "$APP_CANDIDATE" "$previous_app" \
    "$blue_image" "$green_image" "$(state_get PREVIOUS_IMAGE)"
  if is_true "$MANAGE_WORKER"; then
    prune_old_images "$WORKER_IMAGE_REPOSITORY" "$WORKER_CANDIDATE" "$previous_worker" \
      "$(state_get PREVIOUS_WORKER_IMAGE)"
  fi
  find "$LOG_DIR" -type f -name '*.log' -mtime +14 -delete 2>/dev/null || true
  log "deployment succeeded: $TARGET_COMMIT"
}

manual_rollback() {
  local rollback_commit rollback_app rollback_worker
  rollback_commit=$(state_get PREVIOUS_COMMIT)
  rollback_app=$(state_get PREVIOUS_IMAGE)
  rollback_worker=$(state_get PREVIOUS_WORKER_IMAGE)
  [[ -n "$rollback_commit" && -n "$rollback_app" ]] \
    || die "no previous application version recorded"

  if is_true "$BACKUP_BEFORE_DEPLOY"; then
    run_backup "pre-rollback"
  fi
  perform_hot_switch "$rollback_commit" "$rollback_app" "$rollback_worker" \
    || die "rollback candidate failed; current slot remains preferred"
  log "rollback succeeded: $rollback_commit"
}

switch_same_image() {
  local commit app worker active
  commit=$(state_get DEPLOYED_COMMIT)
  active=$(require_active_slot)
  app=$(container_image "$(slot_container "$active")")
  worker=$(container_image "$WORKER_CONTAINER")
  [[ -n "$commit" && -n "$app" ]] || die "current version is not recorded"
  perform_hot_switch "$commit" "$app" "$worker" \
    || die "same-image blue/green switch failed"
  log "same-image switch succeeded"
}

if [[ ${SUB2API_AUTODEPLOY_LIBRARY_MODE:-false} == true ]]; then
  [[ "${BASH_SOURCE[0]}" != "$0" ]] || die "library mode requires sourcing this script"
  return 0
fi

case "$MODE" in
  --deploy|--force|--check|--build-only|--status|--rollback|--switch-same-image) ;;
  *) die "usage: $PROGRAM [--deploy|--force|--check|--build-only|--status|--rollback|--switch-same-image]" ;;
esac

for command_name in git docker curl flock awk grep systemctl ss; do
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
[[ -x "$HAPROXY_CONFIG_COMMAND" ]] \
  || die "HAProxy config command is not executable: $HAPROXY_CONFIG_COMMAND"
grep -Fq 'sub2api-blue:' "$COMPOSE_FILE" \
  || die "Compose file has no blue slot; run the blue/green installer"
grep -Fq 'sub2api-green:' "$COMPOSE_FILE" \
  || die "Compose file has no green slot; run the blue/green installer"
# These are literal Compose interpolation markers, not shell expressions.
# shellcheck disable=SC2016
grep -Fq '${SUB2API_IMAGE' "$COMPOSE_FILE" \
  || die "Compose file is not configured with SUB2API_IMAGE; run the installer"
if is_true "$MANAGE_WORKER"; then
  # shellcheck disable=SC2016
  grep -Fq '${PRODUCT_SYNC_WORKER_IMAGE' "$COMPOSE_FILE" \
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
if [[ "$MODE" == --switch-same-image ]]; then
  switch_same_image
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
  check_migration_compatibility "$(state_get DEPLOYED_COMMIT)" "$TARGET_COMMIT" \
    || die "build stopped by migration compatibility gate"
  build_images
  prune_build_cache
  log "CI-approved images are ready: $APP_CANDIDATE and $WORKER_CANDIDATE"
  exit 0
fi

deploy_approved_commit
