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

printf "ALTER TABLE users ADD COLUMN safe_name text NOT NULL DEFAULT '';\n" \
  >"$REPO/backend/migrations/004_add_defaulted_not_null.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m additive-defaulted-not-null >/dev/null
ADDITIVE_DEFAULT=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$ADDITIVE" "$ADDITIVE_DEFAULT"

cat >"$REPO/backend/migrations/005_replace_trigger.sql" <<'SQL'
DROP TRIGGER IF EXISTS users_rollup ON users;
CREATE TRIGGER users_rollup AFTER INSERT ON users FOR EACH ROW EXECUTE FUNCTION notify_users();
SQL
git -C "$REPO" add .
git -C "$REPO" commit -m replace-trigger >/dev/null
REPLACE_TRIGGER=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$ADDITIVE_DEFAULT" "$REPLACE_TRIGGER"

cat >"$REPO/backend/migrations/006_replace_platform_check.sql" <<'SQL'
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_platform_check;
ALTER TABLE users ADD CONSTRAINT users_platform_check CHECK (display_name IS NULL OR display_name IN ('APP', 'SAMPLE'));
SQL
git -C "$REPO" add .
git -C "$REPO" commit -m replace-check-constraint >/dev/null
REPLACE_CHECK=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$REPLACE_TRIGGER" "$REPLACE_CHECK"

cat >"$REPO/backend/migrations/007_replace_active_index.sql" <<'SQL'
DROP INDEX IF EXISTS users_one_active_idx;
CREATE UNIQUE INDEX IF NOT EXISTS users_one_manual_active_idx
  ON users ((TRUE)) WHERE display_name = 'manual';
CREATE UNIQUE INDEX IF NOT EXISTS users_one_group_active_idx
  ON users (display_name) WHERE display_name <> 'manual';
SQL
git -C "$REPO" add .
git -C "$REPO" commit -m replace-index >/dev/null
REPLACE_INDEX=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$REPLACE_CHECK" "$REPLACE_INDEX"

printf 'DROP INDEX IF EXISTS users_one_manual_active_idx;\n' \
  >"$REPO/backend/migrations/008_drop_index.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-drop-index >/dev/null
DROP_INDEX=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$REPLACE_INDEX" "$DROP_INDEX"; then
  echo 'unpaired DROP INDEX migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users DROP CONSTRAINT IF EXISTS users_platform_check;\n' \
  >"$REPO/backend/migrations/007_drop_constraint.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-drop-constraint >/dev/null
DROP_CONSTRAINT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$REPLACE_CHECK" "$DROP_CONSTRAINT"; then
  echo 'unpaired DROP CONSTRAINT migration was not rejected' >&2
  exit 1
fi

cat >"$REPO/backend/migrations/005_mixed_add_columns.sql" <<'SQL'
ALTER TABLE users
  ADD COLUMN safe_again text NOT NULL DEFAULT '',
  ADD COLUMN unsafe_name text NOT NULL;
SQL
git -C "$REPO" add .
git -C "$REPO" commit -m mixed-add-columns >/dev/null
MIXED_ADD=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$ADDITIVE_DEFAULT" "$MIXED_ADD"; then
  echo 'unsafe ADD COLUMN in a mixed ALTER was not rejected' >&2
  exit 1
fi

printf "COMMENT ON TABLE users IS 'DROP and RENAME are only documentation';\n" \
  >"$REPO/backend/migrations/006_comment_keywords.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m comment-keywords >/dev/null
COMMENT_KEYWORDS=$(git -C "$REPO" rev-parse HEAD)
check_migration_compatibility "$MIXED_ADD" "$COMMENT_KEYWORDS"

printf 'DROP TABLE users;\n' >"$REPO/backend/migrations/007_drop_users.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-drop >/dev/null
DROP_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$COMMENT_KEYWORDS" "$DROP_COMMIT"; then
  echo 'DROP migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ALTER COLUMN display_name TYPE varchar(32);\n' \
  >"$REPO/backend/migrations/008_change_type.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-type >/dev/null
TYPE_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$DROP_COMMIT" "$TYPE_COMMIT"; then
  echo 'column type migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ALTER COLUMN display_name SET NOT NULL;\n' \
  >"$REPO/backend/migrations/009_set_not_null.sql"
git -C "$REPO" add .
git -C "$REPO" commit -m destructive-null >/dev/null
NULL_COMMIT=$(git -C "$REPO" rev-parse HEAD)
if check_migration_compatibility "$TYPE_COMMIT" "$NULL_COMMIT"; then
  echo 'NOT NULL migration was not rejected' >&2
  exit 1
fi

printf 'ALTER TABLE users ADD COLUMN required_name text NOT NULL;\n' \
  >"$REPO/backend/migrations/010_add_not_null.sql"
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
