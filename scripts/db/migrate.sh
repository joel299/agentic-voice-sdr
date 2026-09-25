#!/usr/bin/env bash
# Apply canonical PostgreSQL development migrations and fail on every SQL error.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
if [[ -f "${ROOT_DIR}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env"
  set +a
fi

POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_DB="${POSTGRES_DB:-agentic_voice_sdr_dev}"
POSTGRES_USER="${POSTGRES_USER:-}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
CONTAINER_NAME="${POSTGRES_CONTAINER_NAME:-agentic-postgres-dev}"
MIGRATIONS_DIR="${ROOT_DIR}/db/migrations"

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
if [[ ! -d "${MIGRATIONS_DIR}" ]]; then
  echo "[-] ERROR: Canonical migrations directory is missing: db/migrations." >&2
  exit 1
fi
shopt -s nullglob
sql_files=("${MIGRATIONS_DIR}"/*.sql)
shopt -u nullglob
if [[ ${#sql_files[@]} -eq 0 ]]; then
  echo "[-] ERROR: No canonical SQL migrations found in db/migrations." >&2
  exit 1
fi

if docker inspect "${CONTAINER_NAME}" >/dev/null 2>&1 && [[ "$(docker inspect --format '{{.State.Running}}' "${CONTAINER_NAME}")" == "true" ]]; then
  run_sql_file() {
    docker exec -i -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
      psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" < "$1"
  }
  run_query() {
    docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
      psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -c "$1"
  }
  run_scalar() {
    docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" \
      psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tA -c "$1"
  }
else
  run_sql_file() {
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -X -v ON_ERROR_STOP=1 -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" \
      -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -f "$1"
  }
  run_query() {
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -X -v ON_ERROR_STOP=1 -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" \
      -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -c "$1"
  }
  run_scalar() {
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -X -v ON_ERROR_STOP=1 -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" \
      -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -tA -c "$1"
  }
fi

printf '[+] Checking %d canonical migration(s) from db/migrations.\n' "${#sql_files[@]}"
run_query 'SELECT 1;' >/dev/null
run_query 'CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW());' >/dev/null
for migration in "${sql_files[@]}"; do
  version="$(basename "${migration}")"
  checksum="$(sha256sum "${migration}" | awk '{print $1}')"
  applied_checksum="$(run_scalar "SELECT checksum FROM schema_migrations WHERE version = '${version}';")"
  if [[ -n "${applied_checksum}" ]]; then
    if [[ "${applied_checksum}" != "${checksum}" ]]; then
      echo "[-] ERROR: Applied migration ${version} has changed; refusing to continue." >&2
      exit 1
    fi
    printf '    Already applied %s\n' "${version}"
    continue
  fi
  printf '    Applying %s\n' "${version}"
  run_sql_file "${migration}"
  run_query "INSERT INTO schema_migrations (version, checksum) VALUES ('${version}', '${checksum}');" >/dev/null
done
printf '[+] All canonical PostgreSQL migrations are applied and verified.\n'
