#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
LAUNCHER=$ROOT_DIR/deploy/autodeploy/sub2api-autodeploy-launcher.sh
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

ORIGIN=$TEST_ROOT/origin.git
SEED=$TEST_ROOT/seed
WORK=$TEST_ROOT/work
MARKER=$TEST_ROOT/marker

git init --bare --initial-branch=custom "$ORIGIN" >/dev/null
git init --initial-branch=custom "$SEED" >/dev/null
git -C "$SEED" config user.name test
git -C "$SEED" config user.email test@example.com
mkdir -p "$SEED/deploy/autodeploy"

write_deploy_script() {
  local version=$1
  cat >"$SEED/deploy/autodeploy/sub2api-autodeploy.sh" <<EOF
#!/usr/bin/env bash
printf '%s|%s|%s\n' '$version' "\$0" "\$*" >"\$LAUNCHER_MARKER"
EOF
  chmod 0755 "$SEED/deploy/autodeploy/sub2api-autodeploy.sh"
}

write_deploy_script v1
git -C "$SEED" add .
git -C "$SEED" commit -m v1 >/dev/null
git -C "$SEED" remote add origin "$ORIGIN"
git -C "$SEED" push origin custom:custom custom:deploy/custom >/dev/null
git clone --branch custom "$ORIGIN" "$WORK" >/dev/null

LAUNCHER_MARKER=$MARKER REPO_DIR=$WORK bash "$LAUNCHER" --status first
grep -Fx 'v1|sub2api-autodeploy.sh|--status first' "$MARKER" >/dev/null

write_deploy_script v2
git -C "$SEED" add .
git -C "$SEED" commit -m v2 >/dev/null
git -C "$SEED" push origin custom:custom >/dev/null

LAUNCHER_MARKER=$MARKER REPO_DIR=$WORK bash "$LAUNCHER" --check pending
grep -Fx 'v1|sub2api-autodeploy.sh|--check pending' "$MARKER" >/dev/null

git -C "$SEED" push origin custom:deploy/custom >/dev/null

LAUNCHER_MARKER=$MARKER REPO_DIR=$WORK bash "$LAUNCHER" --deploy second
grep -Fx 'v2|sub2api-autodeploy.sh|--deploy second' "$MARKER" >/dev/null

echo 'autodeploy launcher tests passed'
