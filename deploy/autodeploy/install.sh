#!/usr/bin/env bash
set -Eeuo pipefail

umask 027

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
DEPLOY_NOW=false
MIGRATE_BLUE_GREEN=false
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.yml}
SYNC_REPO_DIR=${SYNC_REPO_DIR:-/opt/sub2api-integration}
STAMP=$(date +%Y%m%d-%H%M%S)
BACKUP_ROOT=${BACKUP_ROOT:-/var/backups/sub2api-blue-green/$STAMP}

for argument in "$@"; do
  case "$argument" in
    --deploy-now) DEPLOY_NOW=true ;;
    --migrate-blue-green) MIGRATE_BLUE_GREEN=true ;;
    *)
      echo "usage: $0 [--migrate-blue-green] [--deploy-now]" >&2
      exit 2
      ;;
  esac
done

[[ $EUID -eq 0 ]] || { echo "run this installer as root" >&2; exit 1; }
[[ -f "$COMPOSE_FILE" ]] || { echo "Compose file not found: $COMPOSE_FILE" >&2; exit 1; }
git -C "$SYNC_REPO_DIR" rev-parse --show-toplevel >/dev/null 2>&1 || {
  echo "Managed update repository not found: $SYNC_REPO_DIR" >&2
  echo "Clone Cloudsflee/sub2api there and check out custom before installing." >&2
  exit 1
}
[[ "$(git -C "$SYNC_REPO_DIR" branch --show-current)" == custom ]] || {
  echo "Managed update repository must be on custom: $SYNC_REPO_DIR" >&2
  exit 1
}

legacy_layout=false
if grep -Fq 'sub2api-blue:' "$COMPOSE_FILE"; then
  grep -Fq 'sub2api-green:' "$COMPOSE_FILE" || {
    echo "Compose contains a blue slot but no green slot; restore the last backup before reinstalling." >&2
    exit 1
  }
else
  legacy_layout=true
  [[ "$MIGRATE_BLUE_GREEN" == true ]] || {
    echo "Compose is still single-slot; rerun with --migrate-blue-green for the controlled handoff." >&2
    exit 1
  }
fi

for script in \
  sub2api-autodeploy-launcher.sh \
  sub2api-autodeploy.sh \
  sub2api-health-restart.sh \
  sub2api-haproxy-config.sh \
  sub2api-blue-green-migrate.sh \
  sub2api-backup.sh \
  sub2api-upstream-sync.sh \
  sub2api-upstream-sync-launcher.sh \
  install.sh; do
  bash -n "$SCRIPT_DIR/$script"
done
python3 -m py_compile "$SCRIPT_DIR/configure-compose.py" \
  "$SCRIPT_DIR/configure-blue-green.py"

mkdir -p "$BACKUP_ROOT"
chmod 700 "$BACKUP_ROOT"
cp -a "$COMPOSE_FILE" "$BACKUP_ROOT/docker-compose.yml.before"
if [[ -f /etc/haproxy/haproxy.cfg ]]; then
  cp -a /etc/haproxy/haproxy.cfg "$BACKUP_ROOT/haproxy.cfg.before"
fi
for installed in \
  /usr/local/sbin/sub2api-autodeploy-launcher \
  /usr/local/sbin/sub2api-autodeploy \
  /usr/local/sbin/sub2api-health-restart.sh \
  /usr/local/sbin/sub2api-haproxy-config \
  /usr/local/sbin/sub2api-blue-green-migrate.sh; do
  if [[ -f "$installed" ]]; then
    cp -a "$installed" "$BACKUP_ROOT/$(basename "$installed").before"
  fi
done

if ! command -v haproxy >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y haproxy
fi

install -m 0755 "$SCRIPT_DIR/sub2api-autodeploy-launcher.sh" /usr/local/sbin/sub2api-autodeploy-launcher
install -m 0755 "$SCRIPT_DIR/sub2api-autodeploy.sh" /usr/local/sbin/sub2api-autodeploy
install -m 0755 "$SCRIPT_DIR/sub2api-health-restart.sh" /usr/local/sbin/sub2api-health-restart.sh
install -m 0755 "$SCRIPT_DIR/sub2api-haproxy-config.sh" /usr/local/sbin/sub2api-haproxy-config
install -m 0755 "$SCRIPT_DIR/sub2api-blue-green-migrate.sh" /usr/local/sbin/sub2api-blue-green-migrate.sh

if [[ -f /usr/local/sbin/sub2api-backup.sh ]] \
  && ! cmp -s "$SCRIPT_DIR/sub2api-backup.sh" /usr/local/sbin/sub2api-backup.sh; then
  cp -a /usr/local/sbin/sub2api-backup.sh \
    "/usr/local/sbin/sub2api-backup.sh.pre-blue-green-$STAMP"
fi
install -m 0755 "$SCRIPT_DIR/sub2api-backup.sh" /usr/local/sbin/sub2api-backup.sh
install -m 0755 "$SCRIPT_DIR/sub2api-upstream-sync.sh" /usr/local/sbin/sub2api-upstream-sync.sh
install -m 0755 "$SCRIPT_DIR/sub2api-upstream-sync-launcher.sh" /usr/local/sbin/sub2api-upstream-sync-launcher.sh

install -m 0644 "$SCRIPT_DIR/sub2api-autodeploy.service" /etc/systemd/system/sub2api-autodeploy.service
install -m 0644 "$SCRIPT_DIR/sub2api-autodeploy.timer" /etc/systemd/system/sub2api-autodeploy.timer
install -m 0644 "$SCRIPT_DIR/sub2api-health-restart.service" /etc/systemd/system/sub2api-health-restart.service
install -m 0644 "$SCRIPT_DIR/sub2api-health-restart.timer" /etc/systemd/system/sub2api-health-restart.timer
install -m 0644 "$SCRIPT_DIR/sub2api-upstream-sync.service" /etc/systemd/system/sub2api-upstream-sync.service
install -m 0644 "$SCRIPT_DIR/sub2api-upstream-sync.timer" /etc/systemd/system/sub2api-upstream-sync.timer

if [[ ! -f /etc/default/sub2api-autodeploy ]]; then
  install -m 0640 "$SCRIPT_DIR/sub2api-autodeploy.default" /etc/default/sub2api-autodeploy
fi

if [[ "$legacy_layout" == true ]]; then
  if [[ -x /usr/local/sbin/sub2api-backup.sh ]]; then
    echo "Creating database/file backup before the Compose conversion..."
    /usr/local/sbin/sub2api-backup.sh
  fi
  if ! python3 "$SCRIPT_DIR/configure-compose.py" "$COMPOSE_FILE" \
    || ! python3 "$SCRIPT_DIR/configure-blue-green.py" --require-worker "$COMPOSE_FILE"; then
    cp -a "$BACKUP_ROOT/docker-compose.yml.before" "$COMPOSE_FILE"
    echo "Compose conversion failed; restored original file. Backup: $BACKUP_ROOT" >&2
    exit 1
  fi
else
  if ! python3 "$SCRIPT_DIR/configure-blue-green.py" --require-worker "$COMPOSE_FILE" \
    || ! python3 "$SCRIPT_DIR/configure-compose.py" "$COMPOSE_FILE"; then
    cp -a "$BACKUP_ROOT/docker-compose.yml.before" "$COMPOSE_FILE"
    echo "Compose conversion failed; restored original file. Backup: $BACKUP_ROOT" >&2
    exit 1
  fi
fi

if ! (cd "$DEPLOY_DIR" && docker compose -f "$COMPOSE_FILE" config >/dev/null); then
  cp -a "$BACKUP_ROOT/docker-compose.yml.before" "$COMPOSE_FILE"
  echo "Compose validation failed; restored original file. Backup: $BACKUP_ROOT" >&2
  exit 1
fi

systemctl daemon-reload

if [[ "$legacy_layout" == true ]]; then
  systemctl stop sub2api-autodeploy.timer sub2api-health-restart.timer \
    sub2api-upstream-sync.timer >/dev/null 2>&1 || true
  LEGACY_COMPOSE_FILE="$BACKUP_ROOT/docker-compose.yml.before" \
    /usr/local/sbin/sub2api-blue-green-migrate.sh
  /usr/local/sbin/sub2api-autodeploy --switch-same-image
  if docker inspect sub2api >/dev/null 2>&1 \
    && [[ "$(docker inspect --format '{{.State.Running}}' sub2api)" != true ]]; then
    docker rm sub2api >/dev/null
  fi
fi

if ! /usr/local/sbin/sub2api-haproxy-config --current >/dev/null 2>&1; then
  active_slot=$(awk -F= '$1 == "ACTIVE_SLOT" { print $2 }' /var/lib/sub2api-autodeploy/state.env 2>/dev/null || true)
  if [[ "$active_slot" != blue && "$active_slot" != green ]]; then
    if docker inspect --format '{{.State.Running}}' sub2api-blue 2>/dev/null | grep -qx true; then
      active_slot=blue
    elif docker inspect --format '{{.State.Running}}' sub2api-green 2>/dev/null | grep -qx true; then
      active_slot=green
    else
      active_slot=blue
    fi
  fi
  /usr/local/sbin/sub2api-haproxy-config --activate "$active_slot"
fi

systemctl enable --now haproxy.service
systemctl enable --now sub2api-autodeploy.timer
systemctl enable --now sub2api-upstream-sync.timer
systemctl enable sub2api-health-restart.timer
systemctl restart sub2api-health-restart.timer

echo "Sub2API blue/green auto-deployment installed."
echo "Timer: systemctl status sub2api-autodeploy.timer"
echo "Status: /usr/local/sbin/sub2api-autodeploy --status"
echo "Logs: journalctl -u sub2api-autodeploy.service"
echo "HAProxy: systemctl status haproxy.service"
echo "Backups: $BACKUP_ROOT"
echo "Upstream sync: journalctl -u sub2api-upstream-sync.service"

if [[ "$DEPLOY_NOW" == true ]]; then
  systemctl start sub2api-autodeploy.service
fi
