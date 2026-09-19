#!/usr/bin/env bash
# ==============================================================================
# Agentic Voice SDR - PostgreSQL Dev Migration Script
# Scope: Applies database migrations in dev environment safely.
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

POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_DB="${POSTGRES_DB:-agentic_voice_sdr_dev}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-}"
CONTAINER_NAME="${POSTGRES_CONTAINER_NAME:-agentic-postgres-dev}"

# Prefer db/migrations if present, fallback to migrations
if [[ -d "${ROOT_DIR}/db/migrations" ]]; then
  MIGRATIONS_DIR="${ROOT_DIR}/db/migrations"
else
  MIGRATIONS_DIR="${ROOT_DIR}/migrations"
fi

echo "[+] Starting PostgreSQL Dev Migration Process..."
echo "    Host: ${POSTGRES_HOST}:${POSTGRES_PORT}"
echo "    Database: ${POSTGRES_DB}"
echo "    User: ${POSTGRES_USER}"

if [[ -z "${POSTGRES_PASSWORD}" ]]; then
  echo "[-] ERROR: POSTGRES_PASSWORD is required in environment." >&2
  exit 1
fi

# Function to execute psql query
run_psql() {
  local query="$1"
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker exec -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" psql -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -c "${query}"
  else
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -c "${query}"
  fi
}

# Function to execute sql file
run_sql_file() {
  local file="$1"
  echo "    Applying migration file: $(basename "${file}")"
  if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker exec -i -e PGPASSWORD="${POSTGRES_PASSWORD}" "${CONTAINER_NAME}" psql -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" < "${file}"
  else
    PGPASSWORD="${POSTGRES_PASSWORD}" psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DB}" -f "${file}"
  fi
}

# Verify database connection
echo "[+] Verifying PostgreSQL connectivity..."
if ! run_psql "SELECT 1;" >/dev/null 2>&1; then
  echo "[-] ERROR: Failed to connect to PostgreSQL database ${POSTGRES_DB} at ${POSTGRES_HOST}:${POSTGRES_PORT}" >&2
  exit 1
fi
echo "[+] PostgreSQL connection verified successfully."

# Apply migrations if directory exists and contains .sql files
if [[ -d "${MIGRATIONS_DIR}" ]]; then
  shopt -s nullglob
  sql_files=("${MIGRATIONS_DIR}"/*.sql)
  shopt -u nullglob

  if [[ ${#sql_files[@]} -gt 0 ]]; then
    echo "[+] Found ${#sql_files[@]} migration file(s) in ${MIGRATIONS_DIR}:"
    for sql_file in $(printf "%s\n" "${sql_files[@]}" | sort); do
      run_sql_file "${sql_file}"
    done
    echo "[+] All migration files applied successfully."
  else
    echo "[!] No .sql migration files found in ${MIGRATIONS_DIR}."
    echo "[!] Pending Stark S1-S5 outbox schema migrations (GRU-69/70/71)."
  fi
else
  echo "[!] Migrations directory ${MIGRATIONS_DIR} does not exist yet."
  echo "[!] Pending Stark S1-S5 outbox schema migrations (GRU-69/70/71)."
fi

echo "[+] Migration process completed."
