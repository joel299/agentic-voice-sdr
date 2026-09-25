BEGIN;

CREATE TABLE agent_prompt_versions (
    version BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at TIMESTAMPTZ NULL,
    CONSTRAINT agent_prompt_versions_version_positive CHECK (version > 0),
    CONSTRAINT agent_prompt_versions_name_valid CHECK (
        char_length(name) <= 120 AND name ~ '[^[:space:]]'
    ),
    CONSTRAINT agent_prompt_versions_prompt_valid CHECK (
        octet_length(prompt) <= 32768 AND prompt ~ '[^[:space:]]'
    ),
    CONSTRAINT agent_prompt_versions_active_has_timestamp CHECK (
        NOT is_active OR activated_at IS NOT NULL
    )
);

CREATE UNIQUE INDEX agent_prompt_versions_one_active_idx
    ON agent_prompt_versions (is_active)
    WHERE is_active;

CREATE FUNCTION prevent_agent_prompt_version_content_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.version IS DISTINCT FROM OLD.version
       OR NEW.name IS DISTINCT FROM OLD.name
       OR NEW.prompt IS DISTINCT FROM OLD.prompt
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'agent prompt version content is immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_prompt_versions_immutable_content
    BEFORE UPDATE ON agent_prompt_versions
    FOR EACH ROW
    EXECUTE FUNCTION prevent_agent_prompt_version_content_update();

COMMIT;
