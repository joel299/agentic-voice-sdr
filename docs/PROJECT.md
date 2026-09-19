# Project Overview — Agentic Voice SDR

## Vision
**Agentic Voice SDR** is an API-first, autonomous outbound voice prospecting system designed for cold B2B lead engagement. Powered by real-time voice streaming with Gemini Live and low-latency telephony integration, it executes natural conversational flows, qualifies leads, schedules demos, and triggers instant multi-channel fallbacks (e.g. WhatsApp).

## Core Flow
```text
Lead Ingestion -> Campaign Dispatcher -> RabbitMQ Command Queue -> Voice Engine -> AudioSocket / Asterisk -> SIP Provider (Fale Paco) -> Lead Phone
                                                                                       |
                                                                              Gemini Live (WSS)
                                                                                       |
State Machine Update <- Postgres / Redis <- Tool Executions / WhatsApp Fallback <- Conversational Analysis
```

## Functional Scope (MVP)

- **Direction**: Outbound only (no inbound call processing in MVP).
- **Concurrency**: 1 concurrent active call per channel instance.
- **Target Call Duration**: 2 to 3 minutes per interaction.
- **Attempt Budget**: Up to 3 dial attempts per lead.
- **Retry Window**: Minimum 1-hour interval, constrained strictly to `America/Sao_Paulo` business hours (09:00 - 18:00 BRT, Mon-Fri).
- **Answering Machine / Voicemail**: Automatic detection -> immediate hangup -> trigger WhatsApp introduction fallback message.
- **Pricing & Negotiation**: Zero price negotiation or custom quote disclosures permitted.
- **Recording & Privacy**: No raw audio recording storage in MVP; transcripts and metadata persisted securely.
- **Persistence**:
  - **PostgreSQL / Supabase**: Durable source of truth for leads, campaigns, call sessions, outbox events, and transcripts.
  - **Redis**: Hot state cache, session locks, rate limiting, and fast-path deduplication.
  - **RabbitMQ**: Asynchronous command queues, event dispatching, fixed-TTL retries, and dead-letter handling.

## Realtime Architecture Constraint
> [!IMPORTANT]
> **Strict Audio Isolation Rule**:
> Redis, RabbitMQ, PostgreSQL, and external integrations (e.g., n8n, webhooks) must **NEVER** process or carry raw PCM/audio frames.
> All audio stream processing is isolated inside the Go Voice Engine between Asterisk AudioSocket and Gemini Live WebSocket.

## Roadmap & Waves
- **Foundation & Wave 001 (Completed)**:
  - Repository governance, agent operating contracts, and quality gates (GRU-56).
  - Infrastructure contracts for Redis caching and RabbitMQ exchange/retry topology (GRU-57).
  - AudioSocket frame codec and TCP stream transport implementation in Go (GRU-59, GRU-60).
  - Local containerized development infrastructure for Redis & RabbitMQ (GRU-61).
- **Wave 002 (In Progress)**:
  - PostgreSQL dev infrastructure & Transactional Outbox pattern (GRU-65).
  - CallSession state machine & domain transition engine.
  - Gemini Live WebSocket bidirectional audio streaming adapter.
