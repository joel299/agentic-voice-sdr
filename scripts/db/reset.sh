#!/usr/bin/env bash
# Destructive reset restricted to a local development database.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [[ -f "${ROOT_DIR}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env"
  set +a
fi

APP_ENV="${APP_ENV:-development}"
POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_DB="${POSTGRES_DB:-agentic_voice_sdr_dev}"
POSTGRES_USER="${POSTGRES_USER:-}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
CONTAINER_NAME="${POSTGRES_CONTAINER_NAME:-agentic-postgres-dev}"

if [[ "${APP_ENV}" != "development" && "${APP_ENV}" != "dev" ]]; then
  echo "[-] FATAL: reset is allowed only when APP_ENV=development or dev." >&2
  exit 1
fi
if [[ "${POSTGRES_HOST}" != "localhost" && "${POSTGRES_HOST}" != "127.0.0.1" ]]; then
  echo "[-] FATAL: reset is allowed only against localhost/127.0.0.1." >&2
  exit 1
fi
if [[ ! "${POSTGRES_DB}" =~ ^[a-zA-Z0-9_]+$ || ( "${POSTGRES_DB}" != *_dev && "${POSTGRES_DB}" != *_development ) ]]; then
  echo "[-] FATAL: database name must be a safe identifier ending in _dev or _development." >&2
  exit 1
fi
if [[ -z "${POSTGRES_USER}" || -z "${POSTGRES_PASSWORD}" ]]; then
  echo "[-] ERROR: POSTGRES_USER and POSTGRES_PASSWORD are required." >&2
  exit 1
fi
case "${POSTGRES_USER}" in
  replace_with_*) echo "[-] ERROR: Replace the PostgreSQL username placeholder in .env." >&2; exit 1 ;;
esac
case "${POSTGRES_PASSWORD}" in
  replace_with_*) echo "[-] ERROR: Replace the PostgreSQL password placeholder in .env." >&2; exit 1 ;;
esac

if docker inspect "${CONTAINER_NAME}" >/dev/null 2>&1 && [[ "$(docker inspect --format '{{.State.Running}}' "${CONTAINER_NAME}")" == "true" ]]; then
  run_maintenance() {
    docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
      psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d postgres -c "$1"
  }
else
  run_maintenance() {
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -X -v ON_ERROR_STOP=1 -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" \
      -U "${POSTGRES_USER}" -d postgres -c "$1"
  }
fi

printf '[!] Resetting local development database %s.\n' "${POSTGRES_DB}"
run_maintenance "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '${POSTGRES_DB}' AND pid <> pg_backend_pid();" >/dev/null
run_maintenance "DROP DATABASE IF EXISTS \"${POSTGRES_DB}\";" >/dev/null
run_maintenance "CREATE DATABASE \"${POSTGRES_DB}\";" >/dev/null
"${SCRIPT_DIR}/migrate.sh"
printf '[+] Development database reset and canonical migrations completed.\n'
