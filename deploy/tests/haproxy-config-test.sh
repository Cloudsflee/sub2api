#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
BIN="$TEST_ROOT/bin"
mkdir -p "$BIN"

cat >"$BIN/haproxy" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == -c ]]; then
  [[ -f "${@: -1}" ]]
else
  exit 0
fi
EOF
cat >"$BIN/systemctl" <<'EOF'
#!/usr/bin/env bash
state=${SYSTEMCTL_STATE:?}
case "$1" in
  is-active) [[ -f "$state" ]] ;;
  reload|start) touch "$state" ;;
  stop) rm -f "$state" ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$BIN/haproxy" "$BIN/systemctl"

CONFIG="$TEST_ROOT/haproxy.cfg"
export SYSTEMCTL_STATE="$TEST_ROOT/haproxy.active"
PATH="$BIN:$PATH" \
  HAPROXY_CONFIG="$CONFIG" \
  "$ROOT_DIR/deploy/autodeploy/sub2api-haproxy-config.sh" --activate blue
grep -Fq '# sub2api-active-slot: blue' "$CONFIG"
grep -Fq 'server sub2api-blue 127.0.0.1:18080 check' "$CONFIG"
grep -Fq 'server sub2api-green 127.0.0.1:18081 check backup' "$CONFIG"

PATH="$BIN:$PATH" \
  HAPROXY_CONFIG="$CONFIG" \
  "$ROOT_DIR/deploy/autodeploy/sub2api-haproxy-config.sh" --activate green
grep -Fq '# sub2api-active-slot: green' "$CONFIG"
grep -Fq 'server sub2api-blue 127.0.0.1:18080 check backup' "$CONFIG"
grep -Fq 'server sub2api-green 127.0.0.1:18081 check' "$CONFIG"
grep -Fq 'timeout client 24h' "$CONFIG"
grep -Fq 'timeout tunnel 24h' "$CONFIG"
grep -Fq 'hard-stop-after 10m' "$CONFIG"
grep -Fq 'http-request del-header X-Forwarded-For' "$CONFIG"
grep -Fq 'http-request del-header X-Forwarded-Host' "$CONFIG"
grep -Fq 'http-request del-header X-Forwarded-Proto' "$CONFIG"
grep -Fq 'http-request del-header X-Forwarded-Port' "$CONFIG"
grep -Fq 'http-request del-header X-Client-IP' "$CONFIG"
grep -Fq 'http-request set-header X-Forwarded-For %[src]' "$CONFIG"
grep -Fq 'http-request set-header X-Forwarded-Port 8080' "$CONFIG"

[[ "$(PATH="$BIN:$PATH" HAPROXY_CONFIG="$CONFIG" \
  "$ROOT_DIR/deploy/autodeploy/sub2api-haproxy-config.sh" --current)" == green ]]
echo 'HAProxy configuration tests passed'
