#!/usr/bin/env bash
set -Eeuo pipefail

SYNC_REPO_DIR=${SYNC_REPO_DIR:-/opt/sub2api-integration}
SYNC_SCRIPT=$SYNC_REPO_DIR/deploy/autodeploy/sub2api-upstream-sync.sh

[[ -f "$SYNC_SCRIPT" && ! -L "$SYNC_SCRIPT" ]] || {
  echo "managed update script not found: $SYNC_SCRIPT" >&2
  exit 1
}

# Keep an open descriptor so a git pull can replace the worktree file safely
# while this invocation continues reading the original script contents.
exec 3<"$SYNC_SCRIPT"
exec bash -c 'source /dev/fd/3' sub2api-upstream-sync.sh "$@"
