ALTER TABLE voice_calls
    ADD COLUMN ai_runtime_status TEXT NOT NULL DEFAULT 'not_started'
        CHECK (ai_runtime_status IN ('not_started','starting','running','failed','degraded','stopped')),
    ADD COLUMN ai_failure_class TEXT
        CHECK (ai_failure_class IS NULL OR ai_failure_class IN ('timeout','canceled','receive_failed','provider_api','media_closed','runtime_error','session_ended'));
