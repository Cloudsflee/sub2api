#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

APP_DIR=${APP_DIR:-/opt/sub2api}
BACKUP_DIR=${BACKUP_DIR:-/opt/backups/sub2api}
RETENTION_DAYS=${RETENTION_DAYS:-7}
STAMP=$(date +%Y%m%d-%H%M%S)
FINAL_DUMP=$BACKUP_DIR/postgres-$STAMP.dump
FINAL_FILES=$BACKUP_DIR/files-$STAMP.tar.gz

mkdir -p "$BACKUP_DIR"
chmod 700 "$(dirname "$BACKUP_DIR")" "$BACKUP_DIR" 2>/dev/null || true

TEMP_DUMP=$(mktemp "$BACKUP_DIR/.postgres-$STAMP.XXXXXX.dump")
TEMP_FILES=$(mktemp "$BACKUP_DIR/.files-$STAMP.XXXXXX.tar.gz")

TEMP_MANIFEST=$(mktemp "$BACKUP_DIR/.manifest-$STAMP.XXXXXX")
TEMP_MANIFEST_AFTER=$(mktemp "$BACKUP_DIR/.manifest-after-$STAMP.XXXXXX")

cleanup() {
  rm -f "$TEMP_DUMP" "$TEMP_FILES" "$TEMP_MANIFEST" "$TEMP_MANIFEST_AFTER"
}
trap cleanup EXIT

cd "$APP_DIR"

docker exec sub2api-postgres sh -c \
  'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' >"$TEMP_DUMP"

# Enumerate restoration inputs explicitly. Atomic replacement of excluded cache
# files changes the parent directory mtime, but must not invalidate unchanged
# restoration inputs. --no-recursion archives each listed entry once.
# Keep tar's normal file-change checks and reject added/removed included paths.
backup_manifest() {
  printf '%s\0' .env docker-compose.yml
  find data \( -path 'data/logs' \
    -o -path 'data/public-account-import-products.json' \
    -o -path 'data/.public-account-import-products-*' \
    -o -path 'data/upstream-sync-request*' \
    -o -path 'data/upstream-sync-status' \) -prune -o -print0
}
for attempt in 1 2 3; do
  backup_manifest | LC_ALL=C sort -z >"$TEMP_MANIFEST"
  if tar --no-recursion --null -czf "$TEMP_FILES" -T "$TEMP_MANIFEST"; then
    backup_manifest | LC_ALL=C sort -z >"$TEMP_MANIFEST_AFTER"
    if cmp -s "$TEMP_MANIFEST" "$TEMP_MANIFEST_AFTER"; then
      break
    fi
    tar_rc=1
    echo 'Included backup paths changed during archive creation' >&2
  else
    tar_rc=$?
  fi
  if [[ "$tar_rc" -ne 1 || "$attempt" -eq 3 ]]; then
    exit "$tar_rc"
  fi
  printf 'Archive changed during attempt %s; retrying\n' "$attempt" >&2
done
tar -tzf "$TEMP_FILES" >/dev/null

[[ -s "$TEMP_DUMP" ]] || { echo "PostgreSQL backup is empty" >&2; exit 1; }
[[ -s "$TEMP_FILES" ]] || { echo "file backup is empty" >&2; exit 1; }

mv "$TEMP_DUMP" "$FINAL_DUMP"
mv "$TEMP_FILES" "$FINAL_FILES"
chmod 600 "$FINAL_DUMP" "$FINAL_FILES"
rm -f "$TEMP_MANIFEST" "$TEMP_MANIFEST_AFTER"
trap - EXIT

find "$BACKUP_DIR" -maxdepth 1 -type f \
  \( -name 'postgres-*.dump' -o -name 'files-*.tar.gz' \) \
  -mtime "+$RETENTION_DAYS" -delete

printf 'database_backup=%s\nfiles_backup=%s\n' "$FINAL_DUMP" "$FINAL_FILES"
