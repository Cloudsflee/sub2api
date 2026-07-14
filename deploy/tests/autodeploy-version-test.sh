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

git -C "$REPO" update-ref refs/tags/upstream/v0.1.154 "$TARGET_COMMIT"
git -C "$REPO" update-ref refs/tags/upstream/v9.0.0-rc1 "$TARGET_COMMIT"
[[ "$(resolve_official_version)" == 0.1.154 ]]

git -C "$REPO" update-ref -d refs/tags/upstream/v0.1.154
git -C "$REPO" update-ref -d refs/tags/upstream/v0.1.153
git -C "$REPO" update-ref -d refs/tags/upstream/v9.0.0-rc1
[[ "$(resolve_official_version)" == 0.1.153 ]]

git -C "$REPO" tag -d v0.1.153 >/dev/null
[[ "$(resolve_official_version)" == 0.1.152 ]]

echo 'autodeploy version tests passed'
