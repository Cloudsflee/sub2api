#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
AUTODEPLOY_SCRIPT=$ROOT_DIR/deploy/autodeploy/sub2api-autodeploy.sh
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

REPO=$TEST_ROOT/repo
mkdir -p "$REPO/backend/cmd/server"
git init --initial-branch=custom "$REPO" >/dev/null
git -C "$REPO" config user.name test
git -C "$REPO" config user.email test@example.com

printf '0.1.152\n' >"$REPO/backend/cmd/server/VERSION"
printf 'base\n' >"$REPO/app.txt"
git -C "$REPO" add .
git -C "$REPO" commit -m base >/dev/null
git -C "$REPO" tag -a v0.1.153 -m v0.1.153

official_tag_object=$(git -C "$REPO" rev-parse refs/tags/v0.1.153)
git -C "$REPO" update-ref refs/tags/upstream/v0.1.153 "$official_tag_object"
printf 'custom\n' >"$REPO/custom.txt"
git -C "$REPO" add custom.txt
git -C "$REPO" commit -m custom >/dev/null

# shellcheck disable=SC1090
SUB2API_AUTODEPLOY_LIBRARY_MODE=true source "$AUTODEPLOY_SCRIPT"
# These globals are consumed by the sourced version resolver.
# shellcheck disable=SC2034
BUILD_DIR=$REPO
# shellcheck disable=SC2034
TARGET_COMMIT=$(git -C "$REPO" rev-parse HEAD)

[[ "$(git -C "$REPO" cat-file -t refs/tags/upstream/v0.1.153)" == tag ]]
[[ "$(resolve_official_version)" == 0.1.153 ]]

# A newer source VERSION wins over an older reachable tag when the host has
# not fetched the newest upstream release tag yet.
printf '0.1.170\n' >"$REPO/backend/cmd/server/VERSION"
[[ "$(resolve_official_version)" == 0.1.170 ]]
printf '0.1.152\n' >"$REPO/backend/cmd/server/VERSION"

git -C "$REPO" update-ref refs/tags/upstream/v0.1.154 "$TARGET_COMMIT"
git -C "$REPO" update-ref refs/tags/upstream/v9.0.0-rc1 "$TARGET_COMMIT"
[[ "$(resolve_official_version)" == 0.1.154 ]]

git -C "$REPO" update-ref -d refs/tags/upstream/v0.1.154
git -C "$REPO" update-ref -d refs/tags/upstream/v0.1.153
git -C "$REPO" update-ref -d refs/tags/upstream/v9.0.0-rc1
[[ "$(resolve_official_version)" == 0.1.153 ]]

git -C "$REPO" tag -d v0.1.153 >/dev/null
[[ "$(resolve_official_version)" == 0.1.152 ]]

REPO_DIR=$REPO
worker_baseline=$(git -C "$REPO" rev-parse HEAD)
mkdir -p "$REPO/deploy/product-sync-worker"
printf 'console.log("worker")\n' >"$REPO/deploy/product-sync-worker/index.js"
git -C "$REPO" add deploy/product-sync-worker/index.js
git -C "$REPO" commit -m 'add worker source' >/dev/null
worker_added=$(git -C "$REPO" rev-parse HEAD)
worker_build_inputs_changed "$worker_baseline" "$worker_added"

printf 'worker docs\n' >"$REPO/deploy/product-sync-worker/README.md"
git -C "$REPO" add deploy/product-sync-worker/README.md
git -C "$REPO" commit -m 'worker docs only' >/dev/null
worker_docs=$(git -C "$REPO" rev-parse HEAD)
if worker_build_inputs_changed "$worker_added" "$worker_docs"; then
  echo 'worker documentation unexpectedly requires an image rebuild' >&2
  exit 1
fi

printf 'test only\n' >"$REPO/deploy/product-sync-worker/index.test.js"
git -C "$REPO" add deploy/product-sync-worker/index.test.js
git -C "$REPO" commit -m 'worker test only' >/dev/null
worker_tests=$(git -C "$REPO" rev-parse HEAD)
if worker_build_inputs_changed "$worker_docs" "$worker_tests"; then
  echo 'worker tests unexpectedly require a production image rebuild' >&2
  exit 1
fi

printf 'application only\n' >>"$REPO/app.txt"
git -C "$REPO" add app.txt
git -C "$REPO" commit -m 'application only' >/dev/null
application_only=$(git -C "$REPO" rev-parse HEAD)
if worker_build_inputs_changed "$worker_tests" "$application_only"; then
  echo 'application changes unexpectedly require a worker image rebuild' >&2
  exit 1
fi

printf 'console.log("worker changed")\n' >"$REPO/deploy/product-sync-worker/index.js"
git -C "$REPO" add deploy/product-sync-worker/index.js
git -C "$REPO" commit -m 'change worker source' >/dev/null
worker_changed=$(git -C "$REPO" rev-parse HEAD)
worker_build_inputs_changed "$application_only" "$worker_changed"
worker_build_inputs_changed missing-commit "$worker_changed"

FAKE_BIN=$TEST_ROOT/bin
DOCKER_ARGS_FILE=$TEST_ROOT/docker-args
mkdir -p "$FAKE_BIN"
cat >"$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$DOCKER_ARGS_FILE"
printf 'Total:\t0B\n'
EOF
chmod +x "$FAKE_BIN/docker"
export DOCKER_ARGS_FILE
PATH="$FAKE_BIN:$PATH"
PRUNE_BUILD_CACHE=true
BUILD_CACHE_MAX_USED_SPACE=6gb
BUILD_CACHE_MIN_FREE_SPACE=8gb
prune_build_cache >/dev/null
mapfile -t docker_args <"$DOCKER_ARGS_FILE"
expected_docker_args=(
  buildx prune --all --force
  --filter 'type!=exec.cachemount'
  --max-used-space 6gb
  --min-free-space 8gb
)
[[ "${docker_args[*]}" == "${expected_docker_args[*]}" ]]

echo 'autodeploy version tests passed'
