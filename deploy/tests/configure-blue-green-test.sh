#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf "$TEST_ROOT"' EXIT

COMPOSE_FILE="$TEST_ROOT/docker-compose.yml"
cat >"$COMPOSE_FILE" <<'EOF'
services:
  sub2api:
    image: demo/sub2api:old
    container_name: sub2api
    ports:
      - "0.0.0.0:8080:8080"
    environment:
      - SERVER_HOST=0.0.0.0
      - SERVER_PORT=8080
    networks:
      - sub2api-network
    healthcheck:
      test: ["CMD", "wget", "-q", "http://localhost:8080/health"]

  product-sync-worker:
    image: demo/worker:old
    container_name: sub2api-product-sync-worker
    environment:
      - BACKEND_URL=http://sub2api:8080
    depends_on:
      sub2api:
        condition: service_healthy
    extra_hosts:
      - "internal.example:host-gateway"
    networks:
      - sub2api-network

networks:
  sub2api-network:
    driver: bridge
EOF

python3 "$ROOT_DIR/deploy/autodeploy/configure-compose.py" "$COMPOSE_FILE"
python3 "$ROOT_DIR/deploy/autodeploy/configure-blue-green.py" --require-worker "$COMPOSE_FILE"
grep -Fq 'x-sub2api-common: &sub2api-common' "$COMPOSE_FILE"
grep -Fq 'sub2api-blue:' "$COMPOSE_FILE"
grep -Fq 'sub2api-green:' "$COMPOSE_FILE"
grep -Fq '127.0.0.1:${SUB2API_BLUE_PORT:-18080}:8080' "$COMPOSE_FILE"
grep -Fq '127.0.0.1:${SUB2API_GREEN_PORT:-18081}:8080' "$COMPOSE_FILE"
grep -Fq 'SERVER_TRUSTED_PROXIES=${SERVER_TRUSTED_PROXIES:-172.18.0.1/32}' "$COMPOSE_FILE"
grep -Fq 'BACKEND_URL=http://host.docker.internal:8080' "$COMPOSE_FILE"
grep -Fq 'internal.example:host-gateway' "$COMPOSE_FILE"
grep -Fq 'host.docker.internal:host-gateway' "$COMPOSE_FILE"
grep -Fq 'host.docker.internal:host-gateway' "$COMPOSE_FILE"
if grep -Fq '0.0.0.0:8080:8080' "$COMPOSE_FILE"; then
  echo 'legacy public port remained in Compose' >&2
  exit 1
fi

FIRST_HASH=$(sha256sum "$COMPOSE_FILE")
python3 "$ROOT_DIR/deploy/autodeploy/configure-blue-green.py" --require-worker "$COMPOSE_FILE"
python3 "$ROOT_DIR/deploy/autodeploy/configure-compose.py" "$COMPOSE_FILE"
SECOND_HASH=$(sha256sum "$COMPOSE_FILE")
[[ "$FIRST_HASH" == "$SECOND_HASH" ]]

echo 'blue/green Compose configuration tests passed'
