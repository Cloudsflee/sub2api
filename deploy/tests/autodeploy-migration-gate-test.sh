#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

REPO="$TEST_ROOT/repo"
STATE_DIR="$TEST_ROOT/state"
mkdir -p "$REPO/backend/migrations" "$STATE_DIR"
git init --initial-branch=custom "$REPO" >/dev/null
git -C "$REPO" config user.name test
git -C "$REPO" config user.email test@example.com

printf 'CREATE TABLE users (id bigint PRIMARY KEY);\n' >"$REPO/backend/migrations/001_init.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m base >/dev/null
BASELINE=$(git -C "$REPO" rev-parse HEAD)

# shellcheck disable=SC1090
SUB2API_AUTODEPLOY_LIBRARY_MODE=true source "$ROOT_DIR/deploy/autodeploy/sub2api-autodeploy.sh"
# These globals are read by the sourced migration gate.
REPO_DIR="$REPO"
STATE_DIR="$STATE_DIR"
STATE_FILE="$STATE_DIR/state.env"

cat >"$REPO/backend/migrations/002_create_jobs.sql" <<'SQL'
CREATE TABLE jobs (
  id bigint PRIMARY KEY,
  status text NOT NULL,
  finished_at timestamptz NULL
);
CREATE INDEX jobs_finished_idx ON jobs (finished_at) WHERE finished_at IS NOT NULL;
SQL
git -C "$REPO" add .
git -C "$REPO" commit -m create-table-not-null >/dev/null
CREATE_TABLE=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$BASELINE" "$CREATE_TABLE"

printf 'ALTER TABLE users ADD COLUMN display_name text;\n' \
  >"$REPO/backend/migrations/003_add_display_name.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m additive >/dev/null
ADDITIVE=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$CREATE_TABLE" "$ADDITIVE"

printf 'DROP TABLE users;\n' >"$REPO/backend/migrations/004_drop_users.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-drop >/dev/null
DROP_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$ADDITIVE" "$DROP_COMMIT"; then
  echo 'DROP migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ALTER COLUMN display_name TYPE varchar(32);\n' \
  >"$REPO/backend/migrations/005_change_type.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-type >/dev/null
TYPE_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$DROP_COMMIT" "$TYPE_COMMIT"; then
  echo 'column type migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ALTER COLUMN display_name SET NOT NULL;\n' \
  >"$REPO/backend/migrations/006_set_not_null.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-null >/dev/null
NULL_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$TYPE_COMMIT" "$NULL_COMMIT"; then
  echo 'NOT NULL migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ADD COLUMN required_name text NOT NULL;\n' \
  >"$REPO/backend/migrations/007_add_not_null.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-add-not-null >/dev/null
ADD_NULL_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$NULL_COMMIT" "$ADD_NULL_COMMIT"; then
  echo 'ADD COLUMN NOT NULL migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ADD COLUMN changed text;\n' >"$REPO/backend/migrations/001_init.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m modify-existing >/dev/null
MODIFIED=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$ADD_NULL_COMMIT" "$MODIFIED"; then
  echo 'modified existing migration was not rejected' >&2
  exit 1
fi

echo 'migration gate tests passed'
