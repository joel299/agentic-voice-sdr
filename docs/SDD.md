# SDD v1.0 — System Design

## Stack
Go 1.27.x, Chi v5, REST/OpenAPI/Scalar, WebSocket events, Asterisk/AudioSocket, Gemini Live, PostgreSQL/Supabase, Redis, RabbitMQ, Tool Registry, Composio, WhatsApp API, Docker, OpenTelemetry.

## Realtime path
Fale Paco SIP -> Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live WSS.

No Redis, RabbitMQ, PostgreSQL or n8n hop is allowed in the PCM path.

## Async path
PostgreSQL transaction -> outbox -> RabbitMQ -> consumers -> Redis invalidation/update.

## RabbitMQ
Exchanges: voice.commands, voice.events, voice.dlx.
Queues: call.dispatch, call.retry, tool.jobs, transcript.persist, voice.dead.

## CallSession
CREATED -> DIALING -> RINGING -> CONNECTED -> CONVERSING -> ENDING -> COMPLETED.
Terminal alternatives: NO_ANSWER, BUSY, VOICEMAIL, FAILED, CANCELED.
