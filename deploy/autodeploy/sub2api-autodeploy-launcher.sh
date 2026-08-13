#!/usr/bin/env bash
set -Eeuo pipefail

REPO_DIR=${REPO_DIR:-/opt/sub2api-custom-src}
REMOTE=${REMOTE:-origin}
SOURCE_BRANCH=${SOURCE_BRANCH:-custom}
DEPLOY_BRANCH=${DEPLOY_BRANCH:-deploy/custom}
DEPLOY_SCRIPT_PATH=${DEPLOY_SCRIPT_PATH:-deploy/autodeploy/sub2api-autodeploy.sh}

source_ref="refs/remotes/$REMOTE/$SOURCE_BRANCH"
deploy_ref="refs/remotes/$REMOTE/$DEPLOY_BRANCH"

git -C "$REPO_DIR" fetch --prune --tags "$REMOTE"
source_commit=$(git -C "$REPO_DIR" rev-parse --verify "$source_ref^{commit}")
approved_commit=$(git -C "$REPO_DIR" rev-parse --verify "$deploy_ref^{commit}")
git -C "$REPO_DIR" merge-base --is-ancestor "$approved_commit" "$source_commit" || {
  echo "approved commit is not contained in $REMOTE/$SOURCE_BRANCH" >&2
  exit 1
}

read -r mode type _ < <(git -C "$REPO_DIR" ls-tree "$approved_commit" -- "$DEPLOY_SCRIPT_PATH")
[[ "$type" == blob && ("$mode" == 100644 || "$mode" == 100755) ]] || {
  echo "approved deployment script not found: $DEPLOY_SCRIPT_PATH" >&2
  exit 1
}

tmp=$(mktemp "${TMPDIR:-/tmp}/sub2api-autodeploy.XXXXXX")
trap 'rm -f "$tmp"' EXIT
git -C "$REPO_DIR" show "$approved_commit:$DEPLOY_SCRIPT_PATH" >"$tmp"
bash -n "$tmp"

# Unlink after opening so the approved script cannot be replaced mid-run.
exec 3<"$tmp"
rm -f "$tmp"
trap - EXIT
exec bash -c 'source /dev/fd/3' sub2api-autodeploy.sh "$@"
