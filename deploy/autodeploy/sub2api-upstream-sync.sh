#!/usr/bin/env bash
set -Eeuo pipefail

umask 027

PROGRAM=${0##*/}
SYNC_REPO_DIR=${SYNC_REPO_DIR:-/opt/sub2api-integration}
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
UPSTREAM_SYNC_REQUEST_FILE=${UPSTREAM_SYNC_REQUEST_FILE:-$DEPLOY_DIR/data/upstream-sync-request}
UPSTREAM_SYNC_STATUS_FILE=${UPSTREAM_SYNC_STATUS_FILE:-$DEPLOY_DIR/data/upstream-sync-status}
ORIGIN_REMOTE=${ORIGIN_REMOTE:-origin}
UPSTREAM_REMOTE=${UPSTREAM_REMOTE:-upstream}
SOURCE_BRANCH=${SOURCE_BRANCH:-custom}
MAIN_BRANCH=${MAIN_BRANCH:-main}
UPSTREAM_SYNC_LOCK_FILE=${UPSTREAM_SYNC_LOCK_FILE:-/run/lock/sub2api-upstream-sync.lock}

PROCESSING_FILE=${UPSTREAM_SYNC_REQUEST_FILE}.processing
FORK_TAG_PREFIX=upstream/
TARGET=
MERGE_STARTED=false
ORIGINAL_CUSTOM_COMMIT=

log() {
  printf '%s [%s] %s\n' "$(date --iso-8601=seconds)" "$PROGRAM" "$*"
}

fail() {
  log "ERROR: $*" >&2
  return 1
}

write_status() {
  local status=$1
  local message=$2
  local commit=${3:-}
  local dir tmp

  dir=$(dirname "$UPSTREAM_SYNC_STATUS_FILE")
  mkdir -p "$dir"
  tmp=$(mktemp "$dir/.upstream-sync-status.XXXXXX")
  message=${message//$'\n'/ }
  cat >"$tmp" <<EOF
status=$status
target=$TARGET
commit=$commit
message=$message
updated_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  chmod 0644 "$tmp"
  mv -f "$tmp" "$UPSTREAM_SYNC_STATUS_FILE"
}

on_error() {
  local rc=$?
  local line=$1
  trap - ERR HUP INT TERM
  if [[ "$MERGE_STARTED" == true ]]; then
    git -C "$SYNC_REPO_DIR" merge --abort >/dev/null 2>&1 || true
  fi
  if [[ -n "$ORIGINAL_CUSTOM_COMMIT" ]]; then
    git -C "$SYNC_REPO_DIR" reset --hard "$ORIGINAL_CUSTOM_COMMIT" >/dev/null 2>&1 || true
  fi
  write_status failed "repository sync failed at line $line (exit $rc)" \
    "$(git -C "$SYNC_REPO_DIR" rev-parse HEAD 2>/dev/null || true)" || true
  rm -f "$PROCESSING_FILE"
  log "repository sync failed at line $line (exit $rc)"
  exit "$rc"
}

on_signal() {
  local signal=$1
  trap - ERR HUP INT TERM
  if [[ "$MERGE_STARTED" == true ]]; then
    git -C "$SYNC_REPO_DIR" merge --abort >/dev/null 2>&1 || true
  fi
  if [[ -n "$ORIGINAL_CUSTOM_COMMIT" ]]; then
    git -C "$SYNC_REPO_DIR" reset --hard "$ORIGINAL_CUSTOM_COMMIT" >/dev/null 2>&1 || true
  fi
  write_status failed "repository sync interrupted by $signal" \
    "$(git -C "$SYNC_REPO_DIR" rev-parse HEAD 2>/dev/null || true)" || true
  rm -f "$PROCESSING_FILE"
  log "repository sync interrupted by $signal"
  exit 1
}

mkdir -p "$(dirname "$UPSTREAM_SYNC_LOCK_FILE")"
exec 9>"$UPSTREAM_SYNC_LOCK_FILE"
if ! flock -n 9; then
  log "another repository sync is active; leaving update request queued"
  exit 0
fi

if [[ ! -e "$UPSTREAM_SYNC_REQUEST_FILE" && -f "$PROCESSING_FILE" && ! -L "$PROCESSING_FILE" ]]; then
  log "recovering interrupted update request"
  mv -f "$PROCESSING_FILE" "$UPSTREAM_SYNC_REQUEST_FILE"
fi

[[ -f "$UPSTREAM_SYNC_REQUEST_FILE" ]] || exit 0
[[ ! -L "$UPSTREAM_SYNC_REQUEST_FILE" ]] || fail "update request must not be a symlink"
mv -f "$UPSTREAM_SYNC_REQUEST_FILE" "$PROCESSING_FILE"
trap 'on_error $LINENO' ERR
trap 'on_signal HUP' HUP
trap 'on_signal INT' INT
trap 'on_signal TERM' TERM

TARGET=$(tr -d '\r\n' <"$PROCESSING_FILE")
[[ "$TARGET" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] \
  || fail "invalid stable release tag: $TARGET"
[[ -d "$SYNC_REPO_DIR/.git" ]] || fail "sync repository not found: $SYNC_REPO_DIR"
[[ "$(git -C "$SYNC_REPO_DIR" branch --show-current)" == "$SOURCE_BRANCH" ]] \
  || fail "sync repository must be on $SOURCE_BRANCH"
[[ -z "$(git -C "$SYNC_REPO_DIR" status --porcelain)" ]] \
  || fail "sync repository contains local changes"

log "fetching fork and official release tag $TARGET"
git -C "$SYNC_REPO_DIR" fetch --prune "$ORIGIN_REMOTE"
git -C "$SYNC_REPO_DIR" pull --ff-only "$ORIGIN_REMOTE" "$SOURCE_BRANCH"
git -C "$SYNC_REPO_DIR" fetch --prune --force "$UPSTREAM_REMOTE" --tags

tag_commit=$(git -C "$SYNC_REPO_DIR" rev-parse --verify "refs/tags/$TARGET^{commit}")
upstream_main=$(git -C "$SYNC_REPO_DIR" rev-parse --verify "refs/remotes/$UPSTREAM_REMOTE/$MAIN_BRANCH^{commit}")
origin_main=$(git -C "$SYNC_REPO_DIR" rev-parse --verify "refs/remotes/$ORIGIN_REMOTE/$MAIN_BRANCH^{commit}")
origin_custom=$(git -C "$SYNC_REPO_DIR" rev-parse --verify "refs/remotes/$ORIGIN_REMOTE/$SOURCE_BRANCH^{commit}")
local_custom=$(git -C "$SYNC_REPO_DIR" rev-parse HEAD)
[[ "$local_custom" == "$origin_custom" ]] \
  || fail "local $SOURCE_BRANCH does not match $ORIGIN_REMOTE/$SOURCE_BRANCH after fast-forward"
ORIGINAL_CUSTOM_COMMIT=$local_custom
git -C "$SYNC_REPO_DIR" merge-base --is-ancestor "$tag_commit" "$upstream_main" \
  || fail "$TARGET is not contained in $UPSTREAM_REMOTE/$MAIN_BRANCH"
git -C "$SYNC_REPO_DIR" merge-base --is-ancestor "$origin_main" "$tag_commit" \
  || fail "$ORIGIN_REMOTE/$MAIN_BRANCH cannot fast-forward to $TARGET"

log "updating fork tracking tag and $MAIN_BRANCH before custom integration"
git -C "$SYNC_REPO_DIR" push "$ORIGIN_REMOTE" \
  "refs/tags/$TARGET:refs/tags/${FORK_TAG_PREFIX}${TARGET}"
git -C "$SYNC_REPO_DIR" push "$ORIGIN_REMOTE" "$tag_commit:refs/heads/$MAIN_BRANCH"

if git -C "$SYNC_REPO_DIR" merge-base --is-ancestor "$tag_commit" HEAD; then
  current=$(git -C "$SYNC_REPO_DIR" rev-parse HEAD)
  write_status current "$TARGET is already contained in $SOURCE_BRANCH" "$current"
  rm -f "$PROCESSING_FILE"
  trap - ERR HUP INT TERM
  log "$TARGET is already present in $SOURCE_BRANCH"
  exit 0
fi

log "merging $TARGET into $SOURCE_BRANCH"
MERGE_STARTED=true
git -C "$SYNC_REPO_DIR" merge --no-ff --no-edit "$tag_commit"
MERGE_STARTED=false
merged_commit=$(git -C "$SYNC_REPO_DIR" rev-parse HEAD)
git -C "$SYNC_REPO_DIR" push "$ORIGIN_REMOTE" "HEAD:refs/heads/$SOURCE_BRANCH"

write_status pushed "$TARGET merged into $SOURCE_BRANCH; waiting for GitHub CI" "$merged_commit"
rm -f "$PROCESSING_FILE"
trap - ERR HUP INT TERM
log "fork update pushed: $merged_commit"
