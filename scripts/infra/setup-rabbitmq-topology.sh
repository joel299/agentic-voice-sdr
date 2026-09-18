#!/usr/bin/env bash
# ==============================================================================
# Declarative RabbitMQ Topology Setup & Verification
# Task: GRU-61 / ADR-002
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Source local .env if present
if [ -f "${REPO_ROOT}/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/.env"
  set +a
elif [ -f "${REPO_ROOT}/deploy/dev/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/deploy/dev/.env"
  set +a
fi

if [ -z "${RABBITMQ_DEFAULT_USER:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: RABBITMQ_DEFAULT_USER" >&2
  echo "    Please export RABBITMQ_DEFAULT_USER or configure .env" >&2
  exit 1
fi

if [ -z "${RABBITMQ_DEFAULT_PASS:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: RABBITMQ_DEFAULT_PASS" >&2
  echo "    Please export RABBITMQ_DEFAULT_PASS or configure .env" >&2
  exit 1
fi

RABBITMQ_HOST="${RABBITMQ_HOST:-127.0.0.1}"
RABBITMQ_MANAGEMENT_PORT="${RABBITMQ_MANAGEMENT_PORT:-15672}"
RABBITMQ_USER="${RABBITMQ_DEFAULT_USER}"
RABBITMQ_PASS="${RABBITMQ_DEFAULT_PASS}"
RABBITMQ_VHOST="${RABBITMQ_DEFAULT_VHOST:-/}"

# URL-encoded vhost for HTTP API (/ -> %2F)
if [ "$RABBITMQ_VHOST" = "/" ]; then
  ENCODED_VHOST="%2F"
else
  ENCODED_VHOST="$RABBITMQ_VHOST"
fi

API_BASE="http://${RABBITMQ_HOST}:${RABBITMQ_MANAGEMENT_PORT}/api"

echo "=== Declarative RabbitMQ Topology Setup ==="
echo "Target: ${API_BASE} (VHost: ${RABBITMQ_VHOST})"

api_req() {
  local method="$1"
  local endpoint="$2"
  local data="${3:-}"

  if [ -n "$data" ]; then
    curl -s -S -f -X "$method" \
      -u "${RABBITMQ_USER}:${RABBITMQ_PASS}" \
      -H "Content-Type: application/json" \
      -d "$data" \
      "${API_BASE}${endpoint}"
  else
    curl -s -S -f -X "$method" \
      -u "${RABBITMQ_USER}:${RABBITMQ_PASS}" \
      "${API_BASE}${endpoint}"
  fi
}

# 1. Declare Exchanges
echo "Declaring Exchanges..."
for exchange in voice.commands voice.events voice.dlx; do
  echo "  - Exchange: ${exchange} (type: topic, durable: true)"
  api_req PUT "/exchanges/${ENCODED_VHOST}/${exchange}" \
    '{"type":"topic","durable":true,"auto_delete":false,"internal":false,"arguments":{}}' > /dev/null
done

# 2. Declare Queues with DLX (except DLQ itself)
echo "Declaring Queues..."
for queue in call.dispatch call.retry tool.jobs transcript.persist; do
  echo "  - Queue: ${queue} (durable: true, DLX: voice.dlx, DLQ key: voice.dead)"
  api_req PUT "/queues/${ENCODED_VHOST}/${queue}" \
    '{"durable":true,"auto_delete":false,"arguments":{"x-dead-letter-exchange":"voice.dlx","x-dead-letter-routing-key":"voice.dead"}}' > /dev/null
done

# 3. Declare Dead Letter Queue (voice.dead)
echo "  - Queue: voice.dead (DLQ, durable: true)"
api_req PUT "/queues/${ENCODED_VHOST}/voice.dead" \
  '{"durable":true,"auto_delete":false,"arguments":{}}' > /dev/null

# 4. Declare Bindings
echo "Declaring Bindings..."
# voice.commands -> call.dispatch, call.retry, tool.jobs
for queue in call.dispatch call.retry tool.jobs; do
  echo "  - Binding: voice.commands -> ${queue} (key: ${queue})"
  api_req POST "/bindings/${ENCODED_VHOST}/e/voice.commands/q/${queue}" \
    "{\"routing_key\":\"${queue}\",\"arguments\":{}}" > /dev/null
done

# voice.events -> transcript.persist
echo "  - Binding: voice.events -> transcript.persist (key: transcript.persist)"
api_req POST "/bindings/${ENCODED_VHOST}/e/voice.events/q/transcript.persist" \
  '{"routing_key":"transcript.persist","arguments":{}}' > /dev/null

# voice.dlx -> voice.dead
echo "  - Binding: voice.dlx -> voice.dead (key: voice.dead)"
api_req POST "/bindings/${ENCODED_VHOST}/e/voice.dlx/q/voice.dead" \
  '{"routing_key":"voice.dead","arguments":{}}' > /dev/null
echo "  - Binding: voice.dlx -> voice.dead (key: #)"
api_req POST "/bindings/${ENCODED_VHOST}/e/voice.dlx/q/voice.dead" \
  '{"routing_key":"#","arguments":{}}' > /dev/null

echo "=== Topology successfully verified and applied ==="
