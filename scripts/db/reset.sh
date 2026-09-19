#!/usr/bin/env bash
# ==============================================================================
# Agentic Voice SDR - Safe PostgreSQL Dev Reset Script
# Scope: Resets dev database by dropping and recreating it, then re-running migrations.
# STRICT SAFETY: Rejects execution in production/staging environments.
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Load .env file if present
if [[ -f "${ROOT_DIR}/.env" ]]; then
  # shellcheck disable=SC1091
  set -a
  source "${ROOT_DIR}/.env"
  set +a
fi

APP_ENV="${APP_ENV:-development}"
POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_DB="${POSTGRES_DB:-agentic_voice_sdr_dev}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
CONTAINER_NAME="${POSTGRES_CONTAINER_NAME:-agentic-postgres-dev}"
FORCE_RESET="${1:-}"

echo "[!] SAFETY CHECK: Verifying environment safety..."

# Safety Check 1: Deny production/staging environment
if [[ "${APP_ENV}" == "production" || "${APP_ENV}" == "prod" || "${APP_ENV}" == "staging" ]]; then
  echo "[-] FATAL: Database reset script execution is STRICTLY PROHIBITED in environment: ${APP_ENV}" >&2
  exit 1
fi

# Safety Check 2: Verify local/dev host unless explicitly forced
if [[ "${POSTGRES_HOST}" != "localhost" && "${POSTGRES_HOST}" != "127.0.0.1" && "${POSTGRES_HOST}" != "postgres" && "${POSTGRES_HOST}" != "${CONTAINER_NAME}" && "${FORCE_RESET}" != "--force-dev-reset" ]]; then
  echo "[-] FATAL: Target host '${POSTGRES_HOST}' is not a local dev host." >&2
  echo "[-] Pass --force-dev-reset as first argument if you are certain this is a local dev instance." >&2
  exit 1
fi

if [[ -z "${POSTGRES_PASSWORD}" ]]; then
  echo "[-] ERROR: POSTGRES_PASSWORD is required in environment." >&2
  exit 1
fi

echo "[!] RESETTING DATABASE: Target database '${POSTGRES_DB}' at ${POSTGRES_HOST}:${POSTGRES_PORT}..."

run_psql_maintenance() {
  local query="$1"
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" psql -U "${POSTGRES_USER}" -d "postgres" -c "${query}"
  else
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "postgres" -c "${query}"
  fi
}

echo "[+] Terminating active connections to '${POSTGRES_DB}'..."
run_psql_maintenance "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '${POSTGRES_DB}' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true

echo "[+] Recreating database '${POSTGRES_DB}'..."
run_psql_maintenance "DROP DATABASE IF EXISTS ${POSTGRES_DB};"
run_psql_maintenance "CREATE DATABASE ${POSTGRES_DB};"

echo "[+] Database reset complete. Re-running migrations..."
"${SCRIPT_DIR}/migrate.sh"

echo "[+] Reset process completed successfully."
