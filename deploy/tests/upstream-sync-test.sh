#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
SYNC_SCRIPT=$ROOT_DIR/deploy/autodeploy/sub2api-upstream-sync.sh
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

UPSTREAM=$TEST_ROOT/upstream.git
ORIGIN=$TEST_ROOT/origin.git
SEED=$TEST_ROOT/seed
WORK=$TEST_ROOT/work
DATA=$TEST_ROOT/data

git init --bare --initial-branch=main "$UPSTREAM" >/dev/null
git init --bare --initial-branch=main "$ORIGIN" >/dev/null
git init --initial-branch=main "$SEED" >/dev/null
git -C "$SEED" config user.name test
git -C "$SEED" config user.email test@example.com

printf 'base\n' >"$SEED/app.txt"
git -C "$SEED" add app.txt
git -C "$SEED" commit -m base >/dev/null
git -C "$SEED" tag -a v0.1.153 -m v0.1.153
git -C "$SEED" remote add upstream "$UPSTREAM"
git -C "$SEED" remote add origin "$ORIGIN"
git -C "$SEED" push upstream main refs/tags/v0.1.153 >/dev/null
git -C "$SEED" push origin main refs/tags/v0.1.153 >/dev/null

git -C "$SEED" switch -c custom >/dev/null
printf 'custom\n' >"$SEED/custom.txt"
git -C "$SEED" add custom.txt
git -C "$SEED" commit -m custom >/dev/null
git -C "$SEED" push origin custom >/dev/null

git -C "$SEED" switch main >/dev/null
printf 'release-154\n' >"$SEED/app.txt"
git -C "$SEED" commit -am release-154 >/dev/null
git -C "$SEED" tag -a v0.1.154 -m v0.1.154
git -C "$SEED" push upstream main refs/tags/v0.1.154 >/dev/null

git clone "$ORIGIN" "$WORK" >/dev/null
git -C "$WORK" switch custom >/dev/null
git -C "$WORK" config user.name test
git -C "$WORK" config user.email test@example.com
git -C "$WORK" remote add upstream "$UPSTREAM"
mkdir -p "$DATA"

run_sync() {
  SYNC_REPO_DIR=$WORK \
  DEPLOY_DIR=$TEST_ROOT \
  UPSTREAM_SYNC_REQUEST_FILE=$DATA/upstream-sync-request \
  UPSTREAM_SYNC_STATUS_FILE=$DATA/upstream-sync-status \
  UPSTREAM_SYNC_LOCK_FILE=$TEST_ROOT/upstream-sync.lock \
    bash "$SYNC_SCRIPT"
}

printf 'v0.1.154\n' >"$DATA/upstream-sync-request"
run_sync

tag_commit=$(git --git-dir="$UPSTREAM" rev-parse 'refs/tags/v0.1.154^{commit}')
origin_main=$(git --git-dir="$ORIGIN" rev-parse refs/heads/main)
origin_custom=$(git --git-dir="$ORIGIN" rev-parse refs/heads/custom)
origin_tracking_tag=$(git --git-dir="$ORIGIN" rev-parse 'refs/tags/upstream/v0.1.154^{commit}')
origin_tracking_type=$(git --git-dir="$ORIGIN" cat-file -t refs/tags/upstream/v0.1.154)
[[ "$origin_main" == "$tag_commit" ]]
[[ "$origin_tracking_tag" == "$tag_commit" ]]
[[ "$origin_tracking_type" == commit ]]
if git --git-dir="$ORIGIN" show-ref --verify --quiet refs/tags/v0.1.154; then
  echo 'official v0.1.154 tag unexpectedly pushed to fork' >&2
  exit 1
fi
git --git-dir="$ORIGIN" merge-base --is-ancestor "$tag_commit" "$origin_custom"
grep -Fx 'status=pushed' "$DATA/upstream-sync-status" >/dev/null
grep -Fx 'target=v0.1.154' "$DATA/upstream-sync-status" >/dev/null

before=$origin_custom
printf 'v0.1.154\n' >"$DATA/upstream-sync-request"
run_sync
after=$(git --git-dir="$ORIGIN" rev-parse refs/heads/custom)
[[ "$before" == "$after" ]]
grep -Fx 'status=current' "$DATA/upstream-sync-status" >/dev/null

# A transient custom push failure must not leave an unpushed merge commit in
# the integration checkout, otherwise a retry could be mistaken for success.
git -C "$SEED" switch main >/dev/null
printf 'release-155\n' >"$SEED/app.txt"
git -C "$SEED" commit -am release-155 >/dev/null
git -C "$SEED" tag -a v0.1.155 -m v0.1.155
git -C "$SEED" push upstream main refs/tags/v0.1.155 >/dev/null

REJECT_MARKER=$TEST_ROOT/reject-custom-once
touch "$REJECT_MARKER"
cat >"$ORIGIN/hooks/update" <<EOF
#!/bin/sh
if [ "\$1" = refs/heads/custom ] && [ -f "$REJECT_MARKER" ]; then
  rm -f "$REJECT_MARKER"
  echo 'rejecting custom once for test' >&2
  exit 1
fi
exit 0
EOF
chmod +x "$ORIGIN/hooks/update"

before_failed_push=$(git --git-dir="$ORIGIN" rev-parse refs/heads/custom)
printf 'v0.1.155\n' >"$DATA/upstream-sync-request"
if run_sync; then
  echo 'rejected custom push unexpectedly succeeded' >&2
  exit 1
fi
after_failed_push=$(git --git-dir="$ORIGIN" rev-parse refs/heads/custom)
local_after_failure=$(git -C "$WORK" rev-parse HEAD)
[[ "$before_failed_push" == "$after_failed_push" ]]
[[ "$local_after_failure" == "$after_failed_push" ]]
grep -Fx 'status=failed' "$DATA/upstream-sync-status" >/dev/null
[[ ! -e "$DATA/upstream-sync-request.processing" ]]

printf 'v0.1.155\n' >"$DATA/upstream-sync-request"
run_sync
origin_custom=$(git --git-dir="$ORIGIN" rev-parse refs/heads/custom)
tag_commit=$(git --git-dir="$UPSTREAM" rev-parse 'refs/tags/v0.1.155^{commit}')
origin_tracking_tag=$(git --git-dir="$ORIGIN" rev-parse 'refs/tags/upstream/v0.1.155^{commit}')
origin_tracking_type=$(git --git-dir="$ORIGIN" cat-file -t refs/tags/upstream/v0.1.155)
[[ "$origin_tracking_tag" == "$tag_commit" ]]
[[ "$origin_tracking_type" == commit ]]
if git --git-dir="$ORIGIN" show-ref --verify --quiet refs/tags/v0.1.155; then
  echo 'official v0.1.155 tag unexpectedly pushed to fork' >&2
  exit 1
fi
git --git-dir="$ORIGIN" merge-base --is-ancestor "$tag_commit" "$origin_custom"
grep -Fx 'status=pushed' "$DATA/upstream-sync-status" >/dev/null

# A request renamed before an interrupted process is recovered on the next run.
printf 'v0.1.155\n' >"$DATA/upstream-sync-request.processing"
run_sync
grep -Fx 'status=current' "$DATA/upstream-sync-status" >/dev/null
[[ ! -e "$DATA/upstream-sync-request.processing" ]]

printf 'upstream/main\n' >"$DATA/upstream-sync-request"
if run_sync; then
  echo 'invalid update target unexpectedly succeeded' >&2
  exit 1
fi
grep -Fx 'status=failed' "$DATA/upstream-sync-status" >/dev/null
[[ ! -e "$DATA/upstream-sync-request.processing" ]]

echo 'upstream sync tests passed'
