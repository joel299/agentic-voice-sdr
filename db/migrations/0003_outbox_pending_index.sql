BEGIN;

CREATE INDEX IF NOT EXISTS idx_outbox_events_pending
    ON outbox_events (created_at ASC)
    WHERE status = 'PENDING';

COMMIT;
