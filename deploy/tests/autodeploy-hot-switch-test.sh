#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT
BIN="$TEST_ROOT/bin"
mkdir -p "$BIN" "$TEST_ROOT/deploy" "$TEST_ROOT/state"

cat >"$BIN/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
root=${FAKE_DOCKER_ROOT:?}
state_file() { printf '%s/%s.state\n' "$root" "$1"; }
read_state() { cat "$(state_file "$1")"; }
write_state() { printf '%s\n' "$2" >"$(state_file "$1")"; }
case "$1" in
  inspect)
    if [[ "$2" == --format ]]; then
      format=$3
      name=$4
      value=$(read_state "$name")
      IFS='|' read -r image running status health restarting <<<"$value"
      case "$format" in
        *Config.Image*) printf '%s\n' "$image" ;;
        *State.Running*) printf '%s\n' "$running" ;;
        *State.Status*) printf '%s\n' "$status" ;;
        *State.Restarting*) printf '%s\n' "$restarting" ;;
        *State.Health*) printf '%s\n' "$health" ;;
        *) printf '\n' ;;
      esac
    else
      [[ -f "$(state_file "$2")" ]]
    fi
    ;;
  compose)
    action=$4
    service=${@: -1}
    case "$action" in
      up)
        image=${SUB2API_IMAGE:-unknown}
        if [[ "$service" == product-sync-worker ]]; then
          image=${PRODUCT_SYNC_WORKER_IMAGE:-unknown}
          write_state sub2api-product-sync-worker "$image|true|running|healthy|false"
        elif [[ "$service" == sub2api-blue ]]; then
          write_state sub2api-blue "$image|true|running|healthy|false"
        elif [[ "$service" == sub2api-green ]]; then
          write_state sub2api-green "$image|true|running|healthy|false"
        fi
        ;;
      stop)
        value=$(read_state "$service")
        IFS='|' read -r image running status health restarting <<<"$value"
        write_state "$service" "$image|false|exited|$health|false"
        ;;
    esac
    ;;
  *) exit 0 ;;
esac
EOF
cat >"$BIN/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$BIN/systemctl" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  is-active) exit 0 ;;
  *) exit 0 ;;
esac
EOF
cat >"$BIN/ss" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat >"$BIN/haproxy-helper" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
case "$1" in
  --current) cat "$FAKE_HAPROXY_SLOT" ;;
  --validate) exit 0 ;;
  --activate) printf '%s\n' "$2" >"$FAKE_HAPROXY_SLOT" ;;
esac
EOF
chmod +x "$BIN"/*

printf 'old|true|running|healthy|false\n' >"$TEST_ROOT/state/sub2api-blue.state"
printf 'unused|false|exited|none|false\n' >"$TEST_ROOT/state/sub2api-green.state"
printf 'worker-old|true|running|healthy|false\n' >"$TEST_ROOT/state/sub2api-product-sync-worker.state"
printf 'blue\n' >"$TEST_ROOT/haproxy-slot"
cat >"$TEST_ROOT/state/state.env" <<'EOF'
DEPLOYED_COMMIT=old-commit
DEPLOYED_IMAGE=old
DEPLOYED_WORKER_IMAGE=worker-old
ACTIVE_SLOT=blue
BLUE_IMAGE=old
GREEN_IMAGE=
PREVIOUS_COMMIT=older-commit
PREVIOUS_IMAGE=older
PREVIOUS_WORKER_IMAGE=worker-older
PREVIOUS_SLOT=green
EOF
printf 'SUB2API_IMAGE=old\nPRODUCT_SYNC_WORKER_IMAGE=worker-old\n' >"$TEST_ROOT/deploy/.env"

SUB2API_AUTODEPLOY_LIBRARY_MODE=true source "$ROOT_DIR/deploy/autodeploy/sub2api-autodeploy.sh"
REPO_DIR="$TEST_ROOT"
DEPLOY_DIR="$TEST_ROOT/deploy"
COMPOSE_FILE="$TEST_ROOT/deploy/docker-compose.yml"
STATE_DIR="$TEST_ROOT/state"
STATE_FILE="$TEST_ROOT/state/state.env"
BLUE_CONTAINER=sub2api-blue
GREEN_CONTAINER=sub2api-green
WORKER_CONTAINER=sub2api-product-sync-worker
HAPROXY_CONFIG_COMMAND="$BIN/haproxy-helper"
HAPROXY_SERVICE=haproxy
PROXY_HEALTH_URL=http://127.0.0.1:8080/health
HEALTH_TIMEOUT_SECONDS=10
HEALTH_CHECK_INTERVAL_SECONDS=0
HEALTH_CONSECUTIVE_SUCCESSES=3
STABILITY_SECONDS=1
WORKER_STABLE_SECONDS=0
DRAIN_POLL_SECONDS=0
DRAIN_TIMEOUT_SECONDS=10
FAKE_DOCKER_ROOT="$TEST_ROOT/state"
FAKE_HAPROXY_SLOT="$TEST_ROOT/haproxy-slot"
export FAKE_DOCKER_ROOT FAKE_HAPROXY_SLOT
PATH="$BIN:$PATH"
touch "$COMPOSE_FILE"

perform_hot_switch new-commit new worker-new

grep -Fxq 'ACTIVE_SLOT=green' "$STATE_FILE"
grep -Fxq 'DEPLOYED_IMAGE=new' "$STATE_FILE"
grep -Fxq 'PREVIOUS_IMAGE=old' "$STATE_FILE"
grep -Fxq 'SUB2API_IMAGE=new' "$DEPLOY_DIR/.env"
grep -q '^old|false|' "$TEST_ROOT/state/sub2api-blue.state"
grep -q '^new|true|' "$TEST_ROOT/state/sub2api-green.state"
grep -Fxq green "$TEST_ROOT/haproxy-slot"
echo 'hot switch tests passed'
