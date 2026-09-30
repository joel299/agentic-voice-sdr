CREATE TABLE voice_calls (
    call_id TEXT PRIMARY KEY,
    destination TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('dialing','ringing','connected','completed','busy','no_answer','failed','canceled')),
    provider TEXT NOT NULL,
    provider_call_id TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    connected_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ,
    terminal_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX voice_calls_provider_call_id_idx ON voice_calls(provider, provider_call_id)
    WHERE provider_call_id IS NOT NULL;

CREATE TABLE voice_call_transcript_turns (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    call_id TEXT NOT NULL REFERENCES voice_calls(call_id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    role TEXT NOT NULL CHECK (role IN ('lead','agent')),
    text TEXT NOT NULL CHECK (length(btrim(text)) > 0),
    transcript_state TEXT NOT NULL CHECK (transcript_state = 'final'),
    source TEXT NOT NULL CHECK (source IN ('gemini_input','gemini_output')),
    idempotency_key TEXT NOT NULL CHECK (length(btrim(idempotency_key)) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (call_id, sequence),
    UNIQUE (call_id, idempotency_key)
);

CREATE INDEX voice_call_transcript_turns_order_idx
    ON voice_call_transcript_turns(call_id, sequence);
