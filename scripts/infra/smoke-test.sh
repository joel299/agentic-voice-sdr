#!/usr/bin/env bash
# ==============================================================================
# Reproducible Smoke Test for Redis + RabbitMQ Dev Infrastructure
# Task: GRU-61 / ADR-002
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DEPLOY_DIR="${REPO_ROOT}/deploy/dev"

# Source local .env if present
if [ -f "${REPO_ROOT}/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/.env"
  set +a
elif [ -f "${DEPLOY_DIR}/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "${DEPLOY_DIR}/.env"
  set +a
fi

# Strict check: fail clearly if required credentials are not in environment
if [ -z "${REDIS_PASSWORD:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: REDIS_PASSWORD" >&2
  echo "    Redis requires a password via environment/secret. Please export REDIS_PASSWORD or configure .env." >&2
  exit 1
fi

if [ -z "${RABBITMQ_DEFAULT_USER:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: RABBITMQ_DEFAULT_USER" >&2
  echo "    RabbitMQ requires an admin user via environment/secret. Please export RABBITMQ_DEFAULT_USER or configure .env." >&2
  exit 1
fi

if [ -z "${RABBITMQ_DEFAULT_PASS:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: RABBITMQ_DEFAULT_PASS" >&2
  echo "    RabbitMQ requires a password via environment/secret. Please export RABBITMQ_DEFAULT_PASS or configure .env." >&2
  exit 1
fi

TEARDOWN=0
for arg in "$@"; do
  if [ "$arg" = "--down" ]; then
    TEARDOWN=1
  fi
done

cleanup() {
  if [ "$TEARDOWN" -eq 1 ]; then
    echo "[SMOKE] Tearing down dev stack (--down requested)..."
    docker compose -f "${DEPLOY_DIR}/docker-compose.yml" down -v
  fi
}
trap cleanup EXIT

echo "======================================================================"
echo " Starting GRU-61 Redis + RabbitMQ Dev Infrastructure Smoke Test"
echo "======================================================================"
echo "Repo Root: ${REPO_ROOT}"
echo "Deploy Dir: ${DEPLOY_DIR}"

# 1. Check prerequisites
if ! command -v docker &> /dev/null; then
  echo "[-] ERROR: docker is not installed or not in PATH."
  exit 1
fi

if ! docker compose version &> /dev/null; then
  echo "[-] ERROR: docker compose is not available."
  exit 1
fi

# 2. Start stack
echo "[+] Starting Docker Compose dev stack..."
docker compose -f "${DEPLOY_DIR}/docker-compose.yml" up -d

# 3. Wait for services to become healthy
echo "[+] Waiting for containers to become healthy (timeout: 90s)..."
wait_healthy() {
  local container="$1"
  local max_attempts=45
  local attempt=1

  while [ "$attempt" -le "$max_attempts" ]; do
    local status
    status=$(docker inspect --format='{{json .State.Health.Status}}' "$container" 2>/dev/null || echo '"not_found"')
    status=$(echo "$status" | tr -d '"')

    if [ "$status" = "healthy" ]; then
      echo "    [✓] Container '${container}' is healthy."
      return 0
    fi

    echo "    [i] Container '${container}' status: '${status}' (attempt ${attempt}/${max_attempts})..."
    sleep 2
    attempt=$((attempt + 1))
  done

  echo "[-] ERROR: Container '${container}' failed to become healthy in time."
  docker logs "$container" --tail 50
  return 1
}

wait_healthy "agentic-redis-dev"
wait_healthy "agentic-rabbitmq-dev"

# 4. Redis Verification
echo ""
echo "======================================================================"
echo " Verifying Redis"
echo "======================================================================"

echo "[+] 4.1. Testing Redis PING with environment password..."
PING_RESP=$(docker exec agentic-redis-dev redis-cli -a "$REDIS_PASSWORD" ping 2>/dev/null || true)
if [ "$PING_RESP" != "PONG" ]; then
  echo "[-] ERROR: Redis PING failed, expected 'PONG', got '${PING_RESP}'"
  exit 1
fi
echo "    [✓] Redis responded PONG."

echo "[+] 4.2. Validating maxmemory and maxmemory-policy..."
MAXMEM=$(docker exec agentic-redis-dev redis-cli -a "$REDIS_PASSWORD" config get maxmemory 2>/dev/null | tail -n 1)
POLICY=$(docker exec agentic-redis-dev redis-cli -a "$REDIS_PASSWORD" config get maxmemory-policy 2>/dev/null | tail -n 1)
echo "    [i] maxmemory: ${MAXMEM} bytes"
echo "    [i] maxmemory-policy: ${POLICY}"

if [ "$POLICY" != "allkeys-lru" ]; then
  echo "[-] ERROR: Expected maxmemory-policy to be 'allkeys-lru', got '${POLICY}'"
  exit 1
fi
echo "    [✓] Redis eviction policy confirmed as allkeys-lru."

echo "[+] 4.3. Validating basic KV operations in volatile cache..."
docker exec agentic-redis-dev redis-cli -a "$REDIS_PASSWORD" set "test:smoke:key" "ok_gru61" ex 60 > /dev/null
VAL=$(docker exec agentic-redis-dev redis-cli -a "$REDIS_PASSWORD" get "test:smoke:key" 2>/dev/null)
if [ "$VAL" != "ok_gru61" ]; then
  echo "[-] ERROR: Redis KV test failed, got '${VAL}'"
  exit 1
fi
docker exec agentic-redis-dev redis-cli -a "$REDIS_PASSWORD" del "test:smoke:key" > /dev/null
echo "    [✓] Redis SET/GET/DEL operations verified."

# 5. RabbitMQ Verification
echo ""
echo "======================================================================"
echo " Verifying RabbitMQ"
echo "======================================================================"

RABBIT_USER="${RABBITMQ_DEFAULT_USER}"
RABBIT_PASS="${RABBITMQ_DEFAULT_PASS}"
RABBIT_PORT="${RABBITMQ_MANAGEMENT_PORT:-15672}"
API_BASE="http://127.0.0.1:${RABBIT_PORT}/api"

echo "[+] 5.1. Testing RabbitMQ node health and diagnostics..."
docker exec agentic-rabbitmq-dev rabbitmq-diagnostics -q ping
docker exec agentic-rabbitmq-dev rabbitmq-diagnostics -q check_port_connectivity
echo "    [✓] RabbitMQ node diagnostics passed."

echo "[+] 5.2. Testing RabbitMQ Management HTTP API aliveness..."
ALIVE_STATUS=$(curl -s -u "${RABBIT_USER}:${RABBIT_PASS}" "${API_BASE}/aliveness-test/%2F" | python3 -c "import sys, json; print(json.load(sys.stdin).get('status', ''))")
if [ "$ALIVE_STATUS" != "ok" ]; then
  echo "[-] ERROR: RabbitMQ aliveness test failed, status: '${ALIVE_STATUS}'"
  exit 1
fi
echo "    [✓] RabbitMQ aliveness check ok."

echo "[+] 5.3. Validating required Exchanges (loaded automatically on startup)..."
EXCHANGES_JSON=$(curl -s -u "${RABBIT_USER}:${RABBIT_PASS}" "${API_BASE}/exchanges/%2F")
for ex in "voice.commands" "voice.events" "voice.dlx"; do
  EXISTS=$(echo "$EXCHANGES_JSON" | python3 -c "import sys, json; exs = [e['name'] for e in json.load(sys.stdin)]; print('${ex}' in exs)")
  if [ "$EXISTS" != "True" ]; then
    echo "[-] ERROR: Missing exchange '${ex}'"
    exit 1
  fi
  echo "    [✓] Exchange '${ex}' confirmed."
done

echo "[+] 5.4. Validating required Queues and DLX configuration (loaded automatically on startup)..."
QUEUES_JSON=$(curl -s -u "${RABBIT_USER}:${RABBIT_PASS}" "${API_BASE}/queues/%2F")
for q in "call.dispatch" "call.retry" "tool.jobs" "transcript.persist" "voice.dead"; do
  EXISTS=$(echo "$QUEUES_JSON" | python3 -c "import sys, json; qs = [x['name'] for x in json.load(sys.stdin)]; print('${q}' in qs)")
  if [ "$EXISTS" != "True" ]; then
    echo "[-] ERROR: Missing queue '${q}'"
    exit 1
  fi

  if [ "$q" != "voice.dead" ]; then
    DLX_CHECK=$(echo "$QUEUES_JSON" | python3 -c "
import sys, json
queues = json.load(sys.stdin)
target = next((x for x in queues if x['name'] == '${q}'), None)
args = target.get('arguments', {}) if target else {}
print(args.get('x-dead-letter-exchange') == 'voice.dlx' and args.get('x-dead-letter-routing-key') == 'voice.dead')
")
    if [ "$DLX_CHECK" != "True" ]; then
      echo "[-] ERROR: Queue '${q}' does not have expected DLX arguments configured."
      exit 1
    fi
    echo "    [✓] Queue '${q}' confirmed (durable, DLX: voice.dlx -> voice.dead)."
  else
    echo "    [✓] Dead Letter Queue '${q}' confirmed (durable)."
  fi
done

echo "[+] 5.5. Testing end-to-end messaging pipeline..."
# Publish message to voice.commands -> call.dispatch
PAYLOAD='{"properties":{},"routing_key":"call.dispatch","payload":"{\"test\":\"smoke_call_dispatch\"}","payload_encoding":"string"}'
curl -s -S -f -u "${RABBIT_USER}:${RABBIT_PASS}" -H "Content-Type: application/json" \
  -d "$PAYLOAD" \
  "${API_BASE}/exchanges/%2F/voice.commands/publish" > /dev/null

# Consume from call.dispatch
CONSUME_PAYLOAD='{"count":1,"ackmode":"ack_requeue_false","encoding":"auto","truncate":50000}'
CONSUMED=$(curl -s -S -f -u "${RABBIT_USER}:${RABBIT_PASS}" -H "Content-Type: application/json" \
  -d "$CONSUME_PAYLOAD" \
  "${API_BASE}/queues/%2F/call.dispatch/get")

MSG_FOUND=$(echo "$CONSUMED" | python3 -c "import sys, json; msgs = json.load(sys.stdin); print(len(msgs) > 0 and 'smoke_call_dispatch' in msgs[0].get('payload', ''))")
if [ "$MSG_FOUND" != "True" ]; then
  echo "[-] ERROR: Failed to consume test message from call.dispatch."
  echo "    Response: ${CONSUMED}"
  exit 1
fi
echo "    [✓] Publishing to voice.commands and consuming from call.dispatch verified."

echo "[+] 5.6. Testing dead letter queue (voice.dlx -> voice.dead)..."
# Publish directly to voice.dlx -> voice.dead
DLX_PAYLOAD='{"properties":{},"routing_key":"voice.dead","payload":"{\"test\":\"smoke_voice_dead\"}","payload_encoding":"string"}'
curl -s -S -f -u "${RABBIT_USER}:${RABBIT_PASS}" -H "Content-Type: application/json" \
  -d "$DLX_PAYLOAD" \
  "${API_BASE}/exchanges/%2F/voice.dlx/publish" > /dev/null

CONSUMED_DLQ=$(curl -s -S -f -u "${RABBIT_USER}:${RABBIT_PASS}" -H "Content-Type: application/json" \
  -d "$CONSUME_PAYLOAD" \
  "${API_BASE}/queues/%2F/voice.dead/get")

DLQ_FOUND=$(echo "$CONSUMED_DLQ" | python3 -c "import sys, json; msgs = json.load(sys.stdin); print(len(msgs) > 0 and 'smoke_voice_dead' in msgs[0].get('payload', ''))")
if [ "$DLQ_FOUND" != "True" ]; then
  echo "[-] ERROR: Failed to consume test message from voice.dead DLQ."
  echo "    Response: ${CONSUMED_DLQ}"
  exit 1
fi
echo "    [✓] Dead Letter Queue routing (voice.dlx -> voice.dead) verified."

echo ""
echo "======================================================================"
echo " [✓] ALL SMOKE TESTS PASSED SUCCESSFULLY (Exit Code: 0)"
echo "======================================================================"
exit 0
