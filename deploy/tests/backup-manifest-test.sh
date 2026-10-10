#!/usr/bin/env bash
set -Eeuo pipefail
ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
export APP_DIR=$TEST_ROOT/app BACKUP_DIR=$TEST_ROOT/backups
mkdir -p "$APP_DIR/data/logs" "$TEST_ROOT/bin"
printf 'config\n' >"$APP_DIR/.env"
printf 'compose\n' >"$APP_DIR/docker-compose.yml"
printf 'shops\n' >"$APP_DIR/data/public-account-import-shops.json"
printf 'worker\n' >"$APP_DIR/data/worker-state.json"
printf 'cache\n' >"$APP_DIR/data/public-account-import-products.json"
printf 'temporary\n' >"$APP_DIR/data/.public-account-import-products-123"
printf 'log\n' >"$APP_DIR/data/logs/runtime.log"
cat >"$TEST_ROOT/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf 'mock PostgreSQL dump\n'
EOF
export REAL_TAR
REAL_TAR=$(command -v tar)
cat >"$TEST_ROOT/bin/tar" <<'EOF'
#!/usr/bin/env bash
if [[ "$*" == *--no-recursion* ]]; then
  # Deterministic atomic cache churn between enumeration and archiving.
  printf 'new cache\n' >data/.public-account-import-products-new
  mv data/.public-account-import-products-new data/public-account-import-products.json
  if [[ "${MUTATE_INCLUDED:-false}" == true ]]; then
    # Every attempt adds an included path after its manifest was captured.
    mktemp data/new-persistent-state.XXXXXX >/dev/null
  fi
fi
exec "$REAL_TAR" "$@"
EOF
chmod +x "$TEST_ROOT/bin/"*
export PATH="$TEST_ROOT/bin:$PATH"
bash "$ROOT_DIR/deploy/autodeploy/sub2api-backup.sh" >"$TEST_ROOT/success.log"
archive=$(find "$BACKUP_DIR" -name 'files-*.tar.gz')
tar -tzf "$archive" >"$TEST_ROOT/contents"
for path in .env docker-compose.yml data/public-account-import-shops.json data/worker-state.json; do
  grep -Fx "$path" "$TEST_ROOT/contents" >/dev/null
done
if grep -E 'products|logs' "$TEST_ROOT/contents"; then
  echo 'Mutable cache/logs leaked into backup' >&2
  exit 1
fi
export BACKUP_DIR=$TEST_ROOT/rejected MUTATE_INCLUDED=true
if bash "$ROOT_DIR/deploy/autodeploy/sub2api-backup.sh" >"$TEST_ROOT/rejected.log" 2>&1; then
  echo 'Backup accepted changing restoration inputs' >&2
  exit 1
fi
[[ -z "$(find "$BACKUP_DIR" -type f -print -quit)" ]]
echo 'backup manifest regression tests passed'
