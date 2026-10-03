-- Keep unknown AI failure provenance explicit rather than calling it shutdown.
ALTER TABLE voice_calls
    DROP CONSTRAINT voice_calls_ai_runtime_stage_check,
    ADD CONSTRAINT voice_calls_ai_runtime_stage_check
        CHECK (ai_runtime_stage IN (
            'not_started','runtime_starting','jev_config','jev_client_init',
            'prompt_snapshot','gemini_input_connect','gemini_response_connect',
            'conversation_state','turn_runtime_init','bridge_init','bridge_run',
            'media_ingress','input_transcription_send','input_transcription_receive',
            'input_transcription_handler','jev_provider','turn_directive',
            'gemini_response_send','gemini_response_receive','media_egress',
            'turn_complete','degraded_mode','runtime_shutdown','runtime_unknown'
        ));
