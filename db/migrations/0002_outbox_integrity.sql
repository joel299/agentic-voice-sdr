BEGIN;

ALTER TABLE outbox_events
    ADD CONSTRAINT outbox_events_idempotency_key_unique UNIQUE (idempotency_key),
    ADD CONSTRAINT outbox_events_status_check
        CHECK (status IN ('PENDING', 'PUBLISHED', 'FAILED')),
    ADD CONSTRAINT outbox_events_retry_count_check
        CHECK (retry_count >= 0),
    ADD CONSTRAINT outbox_events_idempotency_key_nonempty_check
        CHECK (length(btrim(idempotency_key)) > 0),
    ADD CONSTRAINT outbox_events_published_at_consistency_check
        CHECK ((status = 'PUBLISHED') = (published_at IS NOT NULL));

COMMIT;
