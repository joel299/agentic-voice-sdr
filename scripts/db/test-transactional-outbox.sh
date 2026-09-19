#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PSQL_ARGS=()
if [[ -n "${DATABASE_URL:-}" ]]; then
  PSQL_ARGS+=("$DATABASE_URL")
fi

psql_cmd() {
  psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 "$@"
}

command -v psql >/dev/null || {
  printf '%s\n' 'psql is required' >&2
  exit 127
}

for migration in "$ROOT_DIR"/db/migrations/*.sql; do
  printf 'Applying %s\n' "${migration#"$ROOT_DIR"/}"
  psql_cmd -f "$migration" >/dev/null
done

psql_cmd <<'SQL'
BEGIN;

CREATE TEMP TABLE outbox_domain_mutations (
    id UUID PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO outbox_events (
    aggregate_type, aggregate_id, event_type, exchange, routing_key,
    payload, correlation_id, idempotency_key
) VALUES (
    'CallSession', 'gru65-test-aggregate', 'call.dispatch.requested',
    'voice.commands', 'call.dispatch', '{"attempt": 1}'::jsonb,
    '00000000-0000-0000-0000-000000000001', 'gru65-test-duplicate'
);

DO $$
DECLARE
    rejected BOOLEAN := FALSE;
BEGIN
    BEGIN
        INSERT INTO outbox_events (
            aggregate_type, aggregate_id, event_type, exchange, routing_key,
            payload, correlation_id, idempotency_key
        ) VALUES (
            'CallSession', 'gru65-test-aggregate-duplicate', 'call.dispatch.requested',
            'voice.commands', 'call.dispatch', '{}'::jsonb,
            '00000000-0000-0000-0000-000000000002', 'gru65-test-duplicate'
        );
    EXCEPTION WHEN unique_violation THEN
        rejected := TRUE;
    END;
    IF NOT rejected THEN
        RAISE EXCEPTION 'duplicate idempotency key was accepted';
    END IF;
END $$;

DO $$
DECLARE
    rejected BOOLEAN := FALSE;
BEGIN
    BEGIN
        INSERT INTO outbox_events (
            aggregate_type, aggregate_id, event_type, exchange, routing_key,
            payload, correlation_id, idempotency_key, status, retry_count
        ) VALUES (
            'CallSession', 'gru65-test-invalid-state', 'call.dispatch.requested',
            'voice.commands', 'call.dispatch', '{}'::jsonb,
            '00000000-0000-0000-0000-000000000003', 'gru65-test-invalid-state', 'INVALID', 0
        );
    EXCEPTION WHEN check_violation THEN
        rejected := TRUE;
    END;
    IF NOT rejected THEN
        RAISE EXCEPTION 'invalid status was accepted';
    END IF;
END $$;

DO $$
DECLARE
    rejected BOOLEAN := FALSE;
BEGIN
    BEGIN
        INSERT INTO outbox_events (
            aggregate_type, aggregate_id, event_type, exchange, routing_key,
            payload, correlation_id, idempotency_key, retry_count
        ) VALUES (
            'CallSession', 'gru65-test-invalid-retry', 'call.dispatch.requested',
            'voice.commands', 'call.dispatch', '{}'::jsonb,
            '00000000-0000-0000-0000-000000000004', 'gru65-test-invalid-retry', -1
        );
    EXCEPTION WHEN check_violation THEN
        rejected := TRUE;
    END;
    IF NOT rejected THEN
        RAISE EXCEPTION 'negative retry count was accepted';
    END IF;
END $$;

INSERT INTO outbox_events (
    aggregate_type, aggregate_id, event_type, exchange, routing_key,
    payload, correlation_id, idempotency_key, status, retry_count, last_error
) VALUES (
    'CallSession', 'gru65-test-retry', 'call.dispatch.requested',
    'voice.commands', 'call.dispatch', '{"attempt": 2}'::jsonb,
    '00000000-0000-0000-0000-000000000005', 'gru65-test-retry', 'FAILED', 2,
    'temporary broker failure'
);

UPDATE outbox_events
SET status = 'PUBLISHED', published_at = NOW()
WHERE idempotency_key = 'gru65-test-retry';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM outbox_events
        WHERE idempotency_key = 'gru65-test-retry'
          AND status = 'PUBLISHED'
          AND retry_count = 2
          AND published_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'published metadata or retry state was not persisted';
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = current_schema()
          AND indexname = 'idx_outbox_events_pending'
    ) THEN
        RAISE EXCEPTION 'pending index is missing';
    END IF;
END $$;

INSERT INTO outbox_events (
    aggregate_type, aggregate_id, event_type, exchange, routing_key,
    payload, correlation_id, idempotency_key
) VALUES (
    'CallSession', 'gru65-test-pending', 'call.dispatch.requested',
    'voice.commands', 'call.dispatch', '{}'::jsonb,
    '00000000-0000-0000-0000-000000000006', 'gru65-test-pending'
);

-- This is the relay's concurrency-safe selection contract.
SELECT 1 FROM outbox_events
WHERE status = 'PENDING'
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED;

SAVEPOINT atomicity;
INSERT INTO outbox_domain_mutations (id, value)
VALUES ('00000000-0000-0000-0000-000000000007', 'domain mutation');
INSERT INTO outbox_events (
    aggregate_type, aggregate_id, event_type, exchange, routing_key,
    payload, correlation_id, idempotency_key
) VALUES (
    'CallSession', 'gru65-test-rollback', 'call.dispatch.requested',
    'voice.commands', 'call.dispatch', '{}'::jsonb,
    '00000000-0000-0000-0000-000000000007', 'gru65-test-rollback'
);
ROLLBACK TO SAVEPOINT atomicity;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM outbox_domain_mutations
               WHERE id = '00000000-0000-0000-0000-000000000007')
       OR EXISTS (SELECT 1 FROM outbox_events
                  WHERE idempotency_key = 'gru65-test-rollback') THEN
        RAISE EXCEPTION 'rollback left a partial domain/outbox mutation';
    END IF;
END $$;

ROLLBACK;
SQL

printf '%s\n' 'Transactional Outbox database tests passed.'
