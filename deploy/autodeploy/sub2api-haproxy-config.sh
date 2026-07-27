#!/usr/bin/env bash
set -Eeuo pipefail

umask 022

PROGRAM=${0##*/}
HAPROXY_CONFIG=${HAPROXY_CONFIG:-/etc/haproxy/haproxy.cfg}
HAPROXY_SERVICE=${HAPROXY_SERVICE:-haproxy}
HAPROXY_PUBLIC_BIND=${HAPROXY_PUBLIC_BIND:-0.0.0.0:8080}
BLUE_PORT=${BLUE_PORT:-18080}
GREEN_PORT=${GREEN_PORT:-18081}

die() {
  printf '%s: %s\n' "$PROGRAM" "$*" >&2
  exit 1
}

validate_slot() {
  case "$1" in
    blue|green) ;;
    *) die "slot must be blue or green" ;;
  esac
}

render_config() {
  local active_slot=$1
  local destination=$2
  local blue_backup= green_backup=

  validate_slot "$active_slot"
  if [[ "$active_slot" == blue ]]; then
    green_backup=' backup'
  else
    blue_backup=' backup'
  fi

  cat >"$destination" <<EOF
# Managed by sub2api-haproxy-config. Local edits are replaced during a switch.
# sub2api-active-slot: $active_slot
global
    log /dev/log local0
    log /dev/log local1 notice
    stats socket /run/haproxy/admin.sock mode 660 level admin
    stats timeout 30s
    user haproxy
    group haproxy
    hard-stop-after 10m

defaults
    log global
    mode http
    option httplog
    option dontlognull
    option http-keep-alive
    option redispatch
    retries 3
    timeout http-request 30s
    timeout queue 30s
    timeout connect 10s
    timeout client 24h
    timeout client-fin 24h
    timeout server 24h
    timeout server-fin 24h
    timeout tunnel 24h
    timeout check 5s

frontend sub2api_http
    bind $HAPROXY_PUBLIC_BIND

    # The public listener is the trust boundary. Never pass client-supplied
    # forwarding headers to the application.
    http-request del-header Forwarded
    http-request del-header X-Forwarded-For
    http-request del-header X-Real-IP
    http-request del-header X-Forwarded-Proto
    http-request del-header X-Forwarded-Host
    http-request del-header X-Forwarded-Port
    http-request del-header X-Client-IP
    http-request del-header CF-Connecting-IP
    http-request del-header True-Client-IP
    http-request set-header X-Forwarded-For %[src]
    http-request set-header X-Real-IP %[src]
    http-request set-header X-Forwarded-Proto http
    http-request set-header X-Forwarded-Port 8080

    default_backend sub2api_slots

backend sub2api_slots
    option httpchk
    http-check send meth GET uri /health ver HTTP/1.1 hdr Host localhost
    http-check expect status 200
    default-server inter 2s fastinter 1s downinter 2s fall 2 rise 2
    server sub2api-blue 127.0.0.1:$BLUE_PORT check$blue_backup
    server sub2api-green 127.0.0.1:$GREEN_PORT check$green_backup
EOF
}

validate_config() {
  command -v haproxy >/dev/null 2>&1 || die "haproxy is not installed"
  haproxy -c -q -f "$1"
}

render_and_validate() {
  local slot=$1
  local destination=$2
  render_config "$slot" "$destination"
  chmod 0644 "$destination"
  validate_config "$destination"
}

activate_slot() {
  local slot=$1
  local config_dir temporary previous= had_previous=false
  local service_was_active=false

  validate_slot "$slot"
  config_dir=$(dirname "$HAPROXY_CONFIG")
  mkdir -p "$config_dir"
  temporary=$(mktemp "$config_dir/.haproxy.cfg.XXXXXX")
  previous=$(mktemp "$config_dir/.haproxy.previous.XXXXXX")

  cleanup() {
    rm -f "$temporary" "$previous"
  }
  trap "rm -f '$temporary' '$previous'" EXIT

  render_and_validate "$slot" "$temporary"
  if [[ -f "$HAPROXY_CONFIG" ]]; then
    cp -a "$HAPROXY_CONFIG" "$previous"
    had_previous=true
  fi
  if systemctl is-active --quiet "$HAPROXY_SERVICE"; then
    service_was_active=true
  fi

  chown root:root "$temporary" 2>/dev/null || true
  mv -f "$temporary" "$HAPROXY_CONFIG"

  if { [[ "$service_was_active" == true ]] && systemctl reload "$HAPROXY_SERVICE"; } \
    || { [[ "$service_was_active" == false ]] && systemctl start "$HAPROXY_SERVICE"; }; then
    cleanup
    return 0
  fi

  printf '%s: HAProxy failed to activate slot %s; restoring previous configuration\n' \
    "$PROGRAM" "$slot" >&2
  if [[ "$had_previous" == true ]]; then
    mv -f "$previous" "$HAPROXY_CONFIG"
    if [[ "$service_was_active" == true ]]; then
      systemctl reload "$HAPROXY_SERVICE" || true
    else
      systemctl start "$HAPROXY_SERVICE" || true
    fi
  else
    rm -f "$HAPROXY_CONFIG"
    systemctl stop "$HAPROXY_SERVICE" || true
  fi
  cleanup
  return 1
}

current_slot() {
  [[ -f "$HAPROXY_CONFIG" ]] || return 1
  sed -n 's/^# sub2api-active-slot: \(blue\|green\)$/\1/p' "$HAPROXY_CONFIG" \
    | head -n 1
}

MODE=${1:-}
case "$MODE" in
  --render)
    [[ $# -eq 3 ]] || die "usage: $PROGRAM --render SLOT OUTPUT"
    render_config "$2" "$3"
    ;;
  --validate)
    [[ $# -eq 2 ]] || die "usage: $PROGRAM --validate SLOT"
    temporary=$(mktemp)
    trap 'rm -f "$temporary"' EXIT
    render_and_validate "$2" "$temporary"
    ;;
  --activate)
    [[ $# -eq 2 ]] || die "usage: $PROGRAM --activate SLOT"
    activate_slot "$2"
    ;;
  --current)
    [[ $# -eq 1 ]] || die "usage: $PROGRAM --current"
    current_slot
    ;;
  *)
    die "usage: $PROGRAM {--render SLOT OUTPUT|--validate SLOT|--activate SLOT|--current}"
    ;;
esac
