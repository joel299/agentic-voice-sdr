# PostgreSQL Dev Infrastructure & Transactional Outbox

This guide covers the PostgreSQL 16 development service and its operational scripts. The canonical Outbox schema is defined only by the ordered migrations `db/migrations/0001_create_outbox_events.sql`, `db/migrations/0002_outbox_integrity.sql`, and `db/migrations/0003_outbox_pending_index.sql`. These files are the source of truth for `outbox_events`; do not maintain a second DDL contract in documentation.

## Prerequisites and environment

Docker Engine with the Compose plugin is required. Copy `.env.example` to `.env` and replace the PostgreSQL, Redis, and RabbitMQ placeholders with local development credentials. Never commit `.env` or real credentials. `POSTGRES_USER` and `POSTGRES_PASSWORD` must be set; scripts reject the untouched password placeholder.

PostgreSQL variables: `POSTGRES_PORT` (default `5432`), `POSTGRES_DB` (default `agentic_voice_sdr_dev`), `POSTGRES_USER`, `POSTGRES_PASSWORD`, and optionally `POSTGRES_HOST` (default `localhost`), `POSTGRES_CONTAINER_NAME` (default `agentic-postgres-dev`), and `DEV_NETWORK_NAME` (default `voice-dev-net`). The container and network names can be overridden to run an isolated local smoke without changing the normal development stack. Redis and RabbitMQ variables remain documented in `.env.example` and `docs/infra/REDIS_RABBITMQ_DEV.md`.

## PostgreSQL 16 service

Validate Compose configuration without printing resolved environment values:

```bash
docker compose -f deploy/dev/docker-compose.yml config --quiet
```

Start only PostgreSQL, preserving Redis and RabbitMQ services:

```bash
docker compose -f deploy/dev/docker-compose.yml up -d postgres
```

Check health and logs:

```bash
docker inspect --format='{{if .State.Health}}{{.State.Health.Status}}{{else}}unknown{{end}}' agentic-postgres-dev
docker logs agentic-postgres-dev --tail 50
```

Stop only PostgreSQL (the named volume is retained):

```bash
docker compose -f deploy/dev/docker-compose.yml stop postgres
```

## Canonical migrations

`./scripts/db/migrate.sh` applies sorted `db/migrations/*.sql` using `psql` with `ON_ERROR_STOP`; missing migrations, connection failures, and SQL errors fail the command. It does not fall back to a second migrations directory and does not silently report success without applying schema.

```bash
./scripts/db/migrate.sh
```

The migration runner records each applied filename and SHA-256 checksum in `schema_migrations`, skips an unchanged applied migration, and fails if an applied migration file was edited. The canonical `outbox_events` contract uses `PENDING`, `PUBLISHED`, and `FAILED`, `published_at`, aggregate and routing fields, payload/headers, idempotency/correlation/trace metadata, retry count, last error, and creation time. Consult the migration files for exact types, nullability, defaults, constraints, and indexes.

## Development-only reset

`./scripts/db/reset.sh` drops and recreates the configured database before applying canonical migrations. It requires `APP_ENV=development` (default) or `dev`, a localhost host, and a safe database identifier ending in `_dev` or `_development`; it rejects remote hosts and production/staging environments. It never uses `docker compose down -v`.

```bash
./scripts/db/reset.sh
```

Treat this command as destructive to the selected local development database. It is not a production migration or reset tool.

## Real PostgreSQL smoke

The smoke starts only the PostgreSQL 16 service, waits for the healthcheck, applies the canonical migration, verifies `outbox_events`, inserts one event with all required canonical columns, confirms PostgreSQL rejects an invalid insert missing required values, and removes the test event. Optional `--down`/`--clean` stops only the PostgreSQL service and preserves its volume; Redis and RabbitMQ are not stopped or removed.

```bash
./scripts/db/smoke-test.sh
./scripts/db/smoke-test.sh --down
```

The smoke does not print credentials or provider response data. No Outbox relay worker or application persistence layer is implemented by this infrastructure foundation.
