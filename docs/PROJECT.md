# Project Overview — Agentic Voice SDR

## Vision

**Agentic Voice SDR** is an API-first, autonomous outbound voice prospecting system for cold B2B leads. It uses real-time voice streaming and telephony integration to conduct natural conversations, qualify leads, schedule meetings, and trigger approved multi-channel fallbacks such as WhatsApp.

## Core Flow

```text
Lead Ingestion -> Campaign Dispatcher -> Command Queue -> Voice Engine
                                                            |
                                             AudioSocket / Asterisk / SIP
                                                            |
                                                   Gemini Live
                                                            |
State Machine Update <- Persistence <- Tool Execution / WhatsApp Fallback
```

## Functional Scope (MVP)

- **Direction**: outbound only.
- **Concurrency**: one concurrent active call per channel instance.
- **Target call duration**: 2–3 minutes per interaction.
- **Attempt budget**: up to three dial attempts per lead.
- **Retry window**: retry after one hour, only inside configurable business hours, using `America/Sao_Paulo` as the timezone.
- **Business hours**: configurable by policy or campaign; this document does not impose a fixed clock window or weekday schedule.
- **Voicemail**: detect, end the call, and trigger the approved WhatsApp fallback.
- **Pricing**: no price negotiation or custom quote disclosure.
- **Recording and privacy**: no raw audio recording storage in the MVP; transcripts and metadata are persisted according to the approved contracts.

## Realtime Architecture Constraint

> **Strict audio isolation rule:** Redis, RabbitMQ, PostgreSQL, n8n, and external integrations must never process or carry raw PCM/audio frames. Audio stays on the realtime path between the telephony boundary, the Go voice engine, and the approved voice provider connection.

## Roadmap and Waves

### Wave 001 — Foundation

- **GRU-59** → Go API runtime + health endpoints
- **GRU-60** → CallSession state machine
- **GRU-61** → Redis + RabbitMQ development infrastructure
- **GRU-62** → AudioSocket frame codec

### Wave 002 — Core Runtime and Domain Boundaries

- **GRU-63** → AudioSocket stream transport boundary
- **GRU-64** → Retry + business-hours scheduling policy
- **GRU-66** → AudioSocket TCP server lifecycle
- **GRU-67** → Tool Registry domain contract
