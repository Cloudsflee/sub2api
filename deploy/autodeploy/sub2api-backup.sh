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

cleanup() {
  rm -f "$TEMP_DUMP" "$TEMP_FILES"
}
trap cleanup EXIT

cd "$APP_DIR"

docker exec sub2api-postgres sh -c \
  'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' >"$TEMP_DUMP"

# Runtime logs and the product catalog cache are continuously updated and are not
# restoration inputs. Excluding them keeps the archive consistent.
tar \
  --ignore-failed-read \
  --warning=no-file-changed \
  --exclude='data/logs' \
  --exclude='data/public-account-import-products.json' \
  -czf "$TEMP_FILES" \
  .env docker-compose.yml data

[[ -s "$TEMP_DUMP" ]] || { echo "PostgreSQL backup is empty" >&2; exit 1; }
[[ -s "$TEMP_FILES" ]] || { echo "file backup is empty" >&2; exit 1; }

mv "$TEMP_DUMP" "$FINAL_DUMP"
mv "$TEMP_FILES" "$FINAL_FILES"
chmod 600 "$FINAL_DUMP" "$FINAL_FILES"
trap - EXIT

find "$BACKUP_DIR" -maxdepth 1 -type f \
  \( -name 'postgres-*.dump' -o -name 'files-*.tar.gz' \) \
  -mtime "+$RETENTION_DAYS" -delete

printf 'database_backup=%s\nfiles_backup=%s\n' "$FINAL_DUMP" "$FINAL_FILES"
