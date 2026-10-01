ALTER TABLE voice_calls
    ADD COLUMN ai_runtime_stage TEXT NOT NULL DEFAULT 'not_started'
        CHECK (ai_runtime_stage IN (
            'not_started','runtime_starting','jev_config','jev_client_init',
            'prompt_snapshot','gemini_input_connect','gemini_response_connect',
            'conversation_state','turn_runtime_init','bridge_init','bridge_run',
            'media_ingress','input_transcription_send','input_transcription_receive',
            'input_transcription_handler','jev_provider','turn_directive',
            'gemini_response_send','gemini_response_receive','media_egress',
            'turn_complete','degraded_mode','runtime_shutdown'
        )),
    ADD COLUMN ai_failure_at TIMESTAMPTZ;

ALTER TABLE voice_calls
    DROP CONSTRAINT voice_calls_ai_failure_class_check,
    ADD CONSTRAINT voice_calls_ai_failure_class_check
        CHECK (ai_failure_class IS NULL OR ai_failure_class IN (
            'timeout','canceled','receive_failed','provider_api','media_closed',
            'runtime_error','session_ended','jev_config','jev_client_init',
            'prompt_snapshot','gemini_input_connect','gemini_response_connect',
            'conversation_state','turn_runtime_init','bridge_init','media_ingress',
            'input_transcription_send','input_transcription_receive','jev_provider',
            'turn_directive','gemini_response_send','gemini_response_receive',
            'media_egress','provider_transport','runtime_unknown'
        ));
