#!/usr/bin/env bash
# PostgreSQL 16 development service + canonical Transactional Outbox smoke.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
COMPOSE_FILE="${ROOT_DIR}/deploy/dev/docker-compose.yml"
CLEANUP_DOWN=false
if [[ "${1:-}" == "--down" || "${1:-}" == "--clean" ]]; then
  CLEANUP_DOWN=true
elif [[ $# -gt 0 ]]; then
  echo "Usage: $0 [--down|--clean]" >&2
  exit 2
fi

if [[ -f "${ROOT_DIR}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env"
  set +a
fi
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_DB="${POSTGRES_DB:-agentic_voice_sdr_dev}"
APP_ENV="${APP_ENV:-development}"
POSTGRES_USER="${POSTGRES_USER:-}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
CONTAINER_NAME="${POSTGRES_CONTAINER_NAME:-agentic-postgres-dev}"
if [[ "${APP_ENV}" != "development" && "${APP_ENV}" != "dev" ]]; then
  echo "[-] FATAL: smoke test is allowed only when APP_ENV=development or dev." >&2
  exit 1
fi
if [[ ! "${POSTGRES_DB}" =~ ^[a-zA-Z0-9_]+$ || ( "${POSTGRES_DB}" != *_dev && "${POSTGRES_DB}" != *_development ) ]]; then
  echo "[-] FATAL: smoke database name must be a safe identifier ending in _dev or _development." >&2
  exit 1
fi
if [[ -z "${POSTGRES_USER}" || -z "${POSTGRES_PASSWORD}" ]]; then
  echo "[-] ERROR: POSTGRES_USER and POSTGRES_PASSWORD are required in .env/environment." >&2
  exit 1
fi
case "${POSTGRES_USER}" in
  replace_with_*) echo "[-] ERROR: Replace the PostgreSQL username placeholder in .env." >&2; exit 1 ;;
esac
case "${POSTGRES_PASSWORD}" in
  replace_with_*) echo "[-] ERROR: Replace the PostgreSQL password placeholder in .env." >&2; exit 1 ;;
esac
export POSTGRES_PORT POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD

cleanup() {
  local status=$?
  if [[ -n "${SMOKE_EVENT_ID:-}" ]]; then
    docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
      psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" \
      -c "DELETE FROM outbox_events WHERE id = '${SMOKE_EVENT_ID}';" >/dev/null 2>&1 || status=1
  fi
  if [[ "${CLEANUP_DOWN}" == true ]]; then
    # Stop only the PostgreSQL service; do not remove volumes or affect Redis/RabbitMQ.
    docker compose -f "${COMPOSE_FILE}" stop postgres >/dev/null || status=1
  fi
  exit "${status}"
}
trap cleanup EXIT

echo "[+] Starting PostgreSQL 16 development service."
docker compose -f "${COMPOSE_FILE}" up -d postgres

healthy=false
for attempt in $(seq 1 30); do
  status="$(docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{else}}unknown{{end}}' "${CONTAINER_NAME}" 2>/dev/null || true)"
  if [[ "${status}" == healthy ]]; then
    healthy=true
    break
  fi
  sleep 2
done
if [[ "${healthy}" != true ]]; then
  echo "[-] PostgreSQL did not become healthy." >&2
  docker logs "${CONTAINER_NAME}" --tail 50 >&2
  exit 1
fi
echo "[+] PostgreSQL healthcheck passed."

major_version="$(docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tAc \
  "SELECT current_setting('server_version_num')::integer / 10000;")"
if [[ "${major_version}" != 16 ]]; then
  echo "[-] Expected PostgreSQL 16; server major version was ${major_version}." >&2
  exit 1
fi
echo "[+] PostgreSQL major version 16: PASS."

"${SCRIPT_DIR}/migrate.sh"
# Verify an unchanged migration set is safely recognized on a subsequent run.
"${SCRIPT_DIR}/migrate.sh"

table_exists="$(docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tAc \
  "SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'outbox_events');")"
if [[ "${table_exists}" != t ]]; then
  echo "[-] Canonical outbox_events table is missing after migrations." >&2
  exit 1
fi

# Run the canonical constraint/transaction/index harness against the same
# PostgreSQL instance and database through libpq environment variables.
(
  # Ensure the canonical harness cannot be redirected by an inherited URL.
  unset DATABASE_URL
  PGHOST=127.0.0.1 \
  PGPORT="${POSTGRES_PORT}" \
  PGUSER="${POSTGRES_USER}" \
  PGPASSWORD="${POSTGRES_PASSWORD}" \
  PGDATABASE="${POSTGRES_DB}" \
    "${SCRIPT_DIR}/test-transactional-outbox.sh"
)
echo "[+] Canonical transactional Outbox contract harness: PASS."

SMOKE_EVENT_ID="$(docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tAc 'SELECT gen_random_uuid();' | tr -d '[:space:]')"
SMOKE_CORRELATION_ID="$(docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tAc 'SELECT gen_random_uuid();' | tr -d '[:space:]')"
if [[ ! "${SMOKE_EVENT_ID}" =~ ^[0-9a-fA-F-]{36}$ || ! "${SMOKE_CORRELATION_ID}" =~ ^[0-9a-fA-F-]{36}$ ]]; then
  echo "[-] Could not generate smoke UUIDs." >&2
  exit 1
fi

# Valid event explicitly supplies every canonical NOT NULL field, including
# aggregate, routing, idempotency, correlation, payload, and status.
docker exec -i -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" <<SQL >/dev/null
INSERT INTO outbox_events (
  id, aggregate_type, aggregate_id, event_type, exchange, routing_key,
  payload, headers, idempotency_key, correlation_id, status, retry_count
) VALUES (
  '${SMOKE_EVENT_ID}', 'SmokeTest', '${SMOKE_EVENT_ID}', 'postgres.smoke_test',
  'voice.commands', 'call.dispatch', '{"smoke":true}'::jsonb, '{}'::jsonb,
  'postgres-smoke-${SMOKE_EVENT_ID}', '${SMOKE_CORRELATION_ID}', 'PENDING', 0
);
SQL
count="$(docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tAc \
  "SELECT count(*) FROM outbox_events WHERE id = '${SMOKE_EVENT_ID}' AND status = 'PENDING';")"
if [[ "${count}" != 1 ]]; then
  echo "[-] Valid canonical event insert was not found." >&2
  exit 1
fi
echo "[+] Valid canonical Outbox event insert: PASS."

# Missing required canonical fields must be rejected by PostgreSQL NOT NULL.
if docker exec -i -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" <<SQL >/dev/null 2>&1
INSERT INTO outbox_events (event_type, payload) VALUES ('postgres.invalid_smoke', '{}'::jsonb);
SQL
then
  echo "[-] Invalid canonical event unexpectedly succeeded." >&2
  exit 1
fi
echo "[+] Invalid canonical event rejected: PASS."

# Explicit cleanup now, then disarm the EXIT cleanup's second delete.
docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
  psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" \
  -c "DELETE FROM outbox_events WHERE id = '${SMOKE_EVENT_ID}';" >/dev/null
SMOKE_EVENT_ID=""
echo "[+] Smoke event cleanup: PASS."
echo "[+] PostgreSQL canonical Outbox smoke: SUCCESS."
