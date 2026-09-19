# PostgreSQL Dev Infrastructure & Transactional Outbox Operations Guide

## Overview

This document details the PostgreSQL local development service, environment contract, migration/reset tooling, smoke testing, and Transactional Outbox schema foundation for the **Agentic Voice SDR** platform (`GRU-65`).

---

## 1. Quick Start & Operations

### Start PostgreSQL Dev Service
```bash
docker compose -f deploy/dev/docker-compose.yml up -d postgres
```

### Stop PostgreSQL Dev Service
```bash
docker compose -f deploy/dev/docker-compose.yml stop postgres
```

### Inspect Service Health & Logs
```bash
docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{else}}unknown{{end}}' agentic-postgres-dev
docker logs agentic-postgres-dev --tail 50
```

---

## 2. Environment Variables & Contracts

All configuration options are defined in `.env.example`. Local overrides are provided via `.env`:

| Variable Name | Default / Example Value | Description |
|---|---|---|
| `POSTGRES_PORT` | `5432` | Exposed host port for local PostgreSQL |
| `POSTGRES_DB` | `agentic_voice_sdr_dev` | Dev database name |
| `POSTGRES_USER` | `replace_with_dev_postgres_username` | Database user |
| `POSTGRES_PASSWORD` | `replace_with_secure_dev_postgres_password` | Database password (required) |
| `SUPABASE_URL` | `replace_with_dev_supabase_url` | Supabase endpoint URL |
| `SUPABASE_ANON_KEY` | `replace_with_dev_supabase_anon_key` | Supabase anonymous public key |
| `SUPABASE_SERVICE_ROLE_KEY` | `replace_with_dev_supabase_service_role_key` | Supabase service role key |

---

## 3. Database Management Scripts

### Safe Dev Migrations
Applies SQL files from `migrations/*.sql` in alphabetical sequence:
```bash
./scripts/db/migrate.sh
```

### Safe Dev Database Reset
Recreates the development database from scratch and re-runs migrations:
```bash
./scripts/db/reset.sh
```

> **Safety Warning:** `reset.sh` enforces strict safety guards:
> 1. Blocks execution if `APP_ENV` is set to `production`, `prod`, or `staging`.
> 2. Rejects execution against remote database hosts unless `--force-dev-reset` is passed.

### Automated Infrastructure Smoke Test
Validates container startup, healthcheck readiness, database connectivity, migration execution, transaction rollback, and outbox schema constraints:
```bash
./scripts/db/smoke-test.sh
```

---

## 4. Transactional Outbox Pattern Foundation

### Architectural Role
The **Transactional Outbox Pattern** ensures atomic state persistence alongside event emission. Business state changes and outbox event records are saved within the same local database transaction.

### Schema Contract (outbox_events)
When Stark S1–S5 migrations (`GRU-69/70/71`) are applied, the `outbox_events` table guarantees:
- **`id`**: `UUID PRIMARY KEY`
- **`event_type`**: `VARCHAR(255) NOT NULL`
- **`payload`**: `JSONB NOT NULL`
- **`status`**: `VARCHAR(50) NOT NULL DEFAULT 'PENDING'` (`PENDING`, `PROCESSED`, `FAILED`)
- **`created_at`**: `TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP`
- **`processed_at`**: `TIMESTAMPTZ NULL`
- **`retry_count`**: `INT DEFAULT 0`
- **Partial Index**: `idx_outbox_events_pending` on `(created_at) WHERE status = 'PENDING'`

### Scope Notice
> **Important:** *The Go Outbox Relay processor (background worker, polling daemon, RabbitMQ publisher) is NOT implemented in GRU-65.* GRU-65 provides the PostgreSQL infrastructure, environment contract, dev container, healthchecks, scripts, and CI validation. The Go Outbox Relay worker implementation is assigned to subsequent tasks (`GRU-69/70/71`).

---

## 5. Maintenance & Troubleshooting

- **Container Reset & Volume Purge:**
  ```bash
  docker compose -f deploy/dev/docker-compose.yml down -v
  docker compose -f deploy/dev/docker-compose.yml up -d postgres
  ```
- **Direct psql Access:**
  ```bash
  docker exec -it agentic-postgres-dev psql -U postgres -d agentic_voice_sdr_dev
  ```

---
*Signed: — Arquimedes*
