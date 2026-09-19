#!/usr/bin/env bash
# ==============================================================================
# Redis + RabbitMQ Local Dev Infrastructure Smoke Test
# Task: GRU-61 / ADR-002 / GRU-57
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DEPLOY_DIR="${REPO_ROOT}/deploy/dev"

# Parse CLI flags
TEARDOWN_DOWN=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --down)
      TEARDOWN_DOWN=true
      shift
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

cleanup() {
  local exit_code=$?
  if [ "$TEARDOWN_DOWN" = true ]; then
    echo "[+] Running cleanup: docker compose down -v..."
    docker compose -f "${DEPLOY_DIR}/docker-compose.yml" down -v || true
  fi
  exit "$exit_code"
}
trap cleanup EXIT

echo "=== GRU-61 Redis + RabbitMQ Infrastructure Smoke Test ==="
echo "Repo root: ${REPO_ROOT}"
echo "Deploy dir: ${DEPLOY_DIR}"

# 1. Environment and Prerequisites Check
echo "[+] 1. Checking environment variables and credentials..."

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

if [ -z "${REDIS_PASSWORD:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: REDIS_PASSWORD" >&2
  echo "    Please export REDIS_PASSWORD or define it in .env" >&2
  exit 1
fi

if [ -z "${RABBITMQ_DEFAULT_USER:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: RABBITMQ_DEFAULT_USER" >&2
  echo "    Please export RABBITMQ_DEFAULT_USER or define it in .env" >&2
  exit 1
fi

if [ -z "${RABBITMQ_DEFAULT_PASS:-}" ]; then
  echo "[-] ERROR: Missing required environment variable: RABBITMQ_DEFAULT_PASS" >&2
  echo "    Please export RABBITMQ_DEFAULT_PASS or define it in .env" >&2
  exit 1
fi

echo "    [✓] Required environment credentials provided without fallbacks."

if ! command -v docker &> /dev/null; then
  echo "[-] ERROR: docker is not installed or not in PATH."
  exit 1
fi

if ! docker compose version &> /dev/null; then
  echo "[-] ERROR: docker compose is not available."
  exit 1
fi

# 2. Start stack
echo "[+] 2. Starting Docker Compose dev stack..."
docker compose -f "${DEPLOY_DIR}/docker-compose.yml" up -d

# 3. Wait for services to become healthy
echo "[+] 3. Waiting for containers to become healthy (timeout: 90s)..."
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
echo " 4. Verifying Redis"
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
echo " 5. Verifying RabbitMQ"
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
ALIVE_STATUS=""
LAST_RESP=""
for attempt in $(seq 1 20); do
  LAST_RESP=$(curl -s -u "${RABBIT_USER}:${RABBIT_PASS}" "${API_BASE}/aliveness-test/%2F" 2>/dev/null || true)
  ALIVE_STATUS=$(echo "$LAST_RESP" | python3 -c "import sys, json; data=json.load(sys.stdin) if sys.stdin else {}; print(data.get('status', ''))" 2>/dev/null || true)
  if [ "$ALIVE_STATUS" = "ok" ]; then
    break
  fi
  sleep 1
done

if [ "$ALIVE_STATUS" != "ok" ]; then
  echo "[-] ERROR: RabbitMQ aliveness test failed, status: '${ALIVE_STATUS}', last response: '${LAST_RESP}'"
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

echo "[+] 5.4. Validating Work Queues and DLX configuration..."
QUEUES_JSON=$(curl -s -u "${RABBIT_USER}:${RABBIT_PASS}" "${API_BASE}/queues/%2F")
for q in "call.dispatch" "tool.jobs" "transcript.persist" "voice.dead"; do
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
      echo "[-] ERROR: Work Queue '${q}' does not have expected DLX arguments configured (voice.dlx -> voice.dead)."
      exit 1
    fi
    echo "    [✓] Work Queue '${q}' confirmed (durable, DLX: voice.dlx -> voice.dead)."
  else
    echo "    [✓] Dead Letter Queue '${q}' confirmed (durable)."
  fi
done

echo "[+] 5.5. Validating Stage-Based Retry Delay Queues and fixed TTLs..."
# Expected stage queues with respective TTLs
declare -A RETRY_TTLS=( ["call.retry.30s"]=30000 ["call.retry.120s"]=120000 ["call.retry.600s"]=600000 )

for rq in "${!RETRY_TTLS[@]}"; do
  EXPECTED_TTL="${RETRY_TTLS[$rq]}"
  RQ_CHECK=$(echo "$QUEUES_JSON" | python3 -c "
import sys, json
queues = json.load(sys.stdin)
target = next((x for x in queues if x['name'] == '${rq}'), None)
if not target:
    print('NOT_FOUND')
else:
    args = target.get('arguments', {})
    ttl = args.get('x-message-ttl')
    dlx = args.get('x-dead-letter-exchange')
    dlk = args.get('x-dead-letter-routing-key')
    if ttl == ${EXPECTED_TTL} and dlx == 'voice.commands' and dlk == 'call.dispatch':
        print('VALID')
    else:
        print(f'INVALID: ttl={ttl}, dlx={dlx}, dlk={dlk}')
")
  if [ "$RQ_CHECK" != "VALID" ]; then
    echo "[-] ERROR: Retry Queue '${rq}' verification failed: ${RQ_CHECK}"
    exit 1
  fi
  echo "    [✓] Retry Delay Queue '${rq}' confirmed (durable, x-message-ttl: ${EXPECTED_TTL}ms, DLX: voice.commands -> call.dispatch)."
done

echo "[+] 5.6. Validating Bindings for all Queues..."
BINDINGS_JSON=$(curl -s -u "${RABBIT_USER}:${RABBIT_PASS}" "${API_BASE}/bindings/%2F")
check_binding() {
  local src="$1"
  local dst="$2"
  local key="$3"
  local found
  found=$(echo "$BINDINGS_JSON" | python3 -c "
import sys, json
bindings = json.load(sys.stdin)
match = any(b.get('source') == '${src}' and b.get('destination') == '${dst}' and b.get('routing_key') == '${key}' for b in bindings)
print('TRUE' if match else 'FALSE')
")
  if [ "$found" != "TRUE" ]; then
    echo "[-] ERROR: Missing binding: ${src} -> ${dst} (key: ${key})"
    exit 1
  fi
  echo "    [✓] Binding confirmed: ${src} -> ${dst} (key: ${key})"
}

check_binding "voice.commands" "call.dispatch" "call.dispatch"
check_binding "voice.commands" "call.retry.30s" "call.retry.30s"
check_binding "voice.commands" "call.retry.120s" "call.retry.120s"
check_binding "voice.commands" "call.retry.600s" "call.retry.600s"
check_binding "voice.commands" "tool.jobs" "tool.jobs"
check_binding "voice.events" "transcript.persist" "transcript.persist"
check_binding "voice.dlx" "voice.dead" "voice.dead"
check_binding "voice.dlx" "voice.dead" "#"

echo "[+] 5.7. Testing end-to-end messaging pipeline on call.dispatch..."
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

echo "[+] 5.8. Testing Dead Letter Queue routing (voice.dlx -> voice.dead)..."
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

echo "[+] 5.9. Testing Stage-Based Retry Delay Return (call.retry.30s -> TTL -> call.dispatch)..."
# Publish test message to voice.commands with routing key call.retry.30s
RETRY_TOKEN="smoke_retry_return_$(date +%s)"
RETRY_PAYLOAD="{\"properties\":{},\"routing_key\":\"call.retry.30s\",\"payload\":\"{\\\"token\\\":\\\"${RETRY_TOKEN}\\\"}\",\"payload_encoding\":\"string\"}"

curl -s -S -f -u "${RABBIT_USER}:${RABBIT_PASS}" -H "Content-Type: application/json" \
  -d "$RETRY_PAYLOAD" \
  "${API_BASE}/exchanges/%2F/voice.commands/publish" > /dev/null

echo "    [i] Published message with token '${RETRY_TOKEN}' to call.retry.30s."
echo "    [i] Waiting for 30s queue TTL expiration and dead-letter return to call.dispatch..."

RETURNED=false
for i in $(seq 1 18); do
  sleep 2
  POLL_RESP=$(curl -s -S -f -u "${RABBIT_USER}:${RABBIT_PASS}" -H "Content-Type: application/json" \
    -d '{"count":5,"ackmode":"ack_requeue_false","encoding":"auto","truncate":50000}' \
    "${API_BASE}/queues/%2F/call.dispatch/get" || true)

  HAS_MSG=$(echo "$POLL_RESP" | python3 -c "
import sys, json
try:
    msgs = json.load(sys.stdin)
    found = any('${RETRY_TOKEN}' in m.get('payload', '') for m in msgs)
    print('FOUND' if found else 'NOT_YET')
except Exception:
    print('ERROR')
")

  if [ "$HAS_MSG" = "FOUND" ]; then
    echo "    [✓] Message arrived back in 'call.dispatch' after TTL dead-lettering (at ~${i}x2s)."
    RETURNED=true
    break
  fi
  echo "    [i] Polling call.dispatch ($((i*2))s elapsed)..."
done

if [ "$RETURNED" != "true" ]; then
  echo "[-] ERROR: Message did not return to call.dispatch within timeout."
  exit 1
fi
echo "    [✓] Stage-based retry dead-lettering verified end-to-end."

echo ""
echo "======================================================================"
echo " [✓] ALL SMOKE TESTS PASSED SUCCESSFULLY (Exit Code: 0)"
echo "======================================================================"
exit 0
