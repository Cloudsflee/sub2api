#!/usr/bin/env bash
set -Eeuo pipefail

umask 027

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
DEPLOY_NOW=false
DEPLOY_DIR=${DEPLOY_DIR:-/opt/sub2api}
COMPOSE_FILE=${COMPOSE_FILE:-$DEPLOY_DIR/docker-compose.yml}
SYNC_REPO_DIR=${SYNC_REPO_DIR:-/opt/sub2api-integration}
STAMP=$(date +%Y%m%d-%H%M%S)

if [[ ${1:-} == --deploy-now ]]; then
  DEPLOY_NOW=true
elif [[ $# -gt 0 ]]; then
  echo "usage: $0 [--deploy-now]" >&2
  exit 2
fi

[[ $EUID -eq 0 ]] || { echo "run this installer as root" >&2; exit 1; }
[[ -f "$COMPOSE_FILE" ]] || { echo "Compose file not found: $COMPOSE_FILE" >&2; exit 1; }
[[ -d "$SYNC_REPO_DIR/.git" ]] || {
  echo "Managed update repository not found: $SYNC_REPO_DIR" >&2
  echo "Clone Cloudsflee/sub2api there and check out custom before installing." >&2
  exit 1
}
[[ "$(git -C "$SYNC_REPO_DIR" branch --show-current)" == custom ]] || {
  echo "Managed update repository must be on custom: $SYNC_REPO_DIR" >&2
  exit 1
}

for script in sub2api-autodeploy.sh sub2api-health-restart.sh sub2api-backup.sh sub2api-upstream-sync.sh install.sh; do
  bash -n "$SCRIPT_DIR/$script"
done
python3 -m py_compile "$SCRIPT_DIR/configure-compose.py"

install -m 0755 "$SCRIPT_DIR/sub2api-autodeploy.sh" /usr/local/sbin/sub2api-autodeploy

if [[ -f /usr/local/sbin/sub2api-health-restart.sh ]] \
  && ! cmp -s "$SCRIPT_DIR/sub2api-health-restart.sh" /usr/local/sbin/sub2api-health-restart.sh; then
  cp -a /usr/local/sbin/sub2api-health-restart.sh \
    "/usr/local/sbin/sub2api-health-restart.sh.pre-autodeploy-$STAMP"
fi
install -m 0755 "$SCRIPT_DIR/sub2api-health-restart.sh" /usr/local/sbin/sub2api-health-restart.sh

if [[ -f /usr/local/sbin/sub2api-backup.sh ]] \
  && ! cmp -s "$SCRIPT_DIR/sub2api-backup.sh" /usr/local/sbin/sub2api-backup.sh; then
  cp -a /usr/local/sbin/sub2api-backup.sh \
    "/usr/local/sbin/sub2api-backup.sh.pre-autodeploy-$STAMP"
fi
install -m 0755 "$SCRIPT_DIR/sub2api-backup.sh" /usr/local/sbin/sub2api-backup.sh
install -m 0755 "$SCRIPT_DIR/sub2api-upstream-sync.sh" /usr/local/sbin/sub2api-upstream-sync.sh

install -m 0644 "$SCRIPT_DIR/sub2api-autodeploy.service" /etc/systemd/system/sub2api-autodeploy.service
install -m 0644 "$SCRIPT_DIR/sub2api-autodeploy.timer" /etc/systemd/system/sub2api-autodeploy.timer
install -m 0644 "$SCRIPT_DIR/sub2api-health-restart.service" /etc/systemd/system/sub2api-health-restart.service
install -m 0644 "$SCRIPT_DIR/sub2api-upstream-sync.service" /etc/systemd/system/sub2api-upstream-sync.service
install -m 0644 "$SCRIPT_DIR/sub2api-upstream-sync.timer" /etc/systemd/system/sub2api-upstream-sync.timer

if [[ ! -f /etc/default/sub2api-autodeploy ]]; then
  install -m 0640 "$SCRIPT_DIR/sub2api-autodeploy.default" /etc/default/sub2api-autodeploy
fi

cp -a "$COMPOSE_FILE" "$COMPOSE_FILE.pre-autodeploy-$STAMP"
python3 "$SCRIPT_DIR/configure-compose.py" "$COMPOSE_FILE"

if ! (cd "$DEPLOY_DIR" && docker compose -f "$COMPOSE_FILE" config >/dev/null); then
  cp -a "$COMPOSE_FILE.pre-autodeploy-$STAMP" "$COMPOSE_FILE"
  echo "Compose validation failed; restored original file" >&2
  exit 1
fi

systemctl daemon-reload
systemctl enable --now sub2api-autodeploy.timer
systemctl enable --now sub2api-upstream-sync.timer
systemctl restart sub2api-health-restart.timer

echo "Sub2API auto-deployment installed."
echo "Timer: systemctl status sub2api-autodeploy.timer"
echo "Status: /usr/local/sbin/sub2api-autodeploy --status"
echo "Logs: journalctl -u sub2api-autodeploy.service"
echo "Upstream sync: journalctl -u sub2api-upstream-sync.service"

if [[ "$DEPLOY_NOW" == true ]]; then
  systemctl start sub2api-autodeploy.service
fi
