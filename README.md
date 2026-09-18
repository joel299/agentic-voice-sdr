# Agentic Voice SDR

![Version](https://img.shields.io/badge/version-v0.1.0--alpha.1-orange)
![Go](https://img.shields.io/badge/Go-1.27.x-00ADD8)
![Redis](https://img.shields.io/badge/Cache-Redis-DC382D)
![RabbitMQ](https://img.shields.io/badge/Broker-RabbitMQ-FF6600)
![PostgreSQL](https://img.shields.io/badge/Database-PostgreSQL-336791)
![Gemini Live](https://img.shields.io/badge/AI-Gemini%20Live-4285F4)

API-first autonomous outbound voice SDR for cold leads.

## MVP

- Outbound only
- 1 concurrent call
- Target call duration: 2–3 minutes
- Up to 3 attempts
- Retry after 1 hour within America/Sao_Paulo business hours
- Natural PT-BR conversation with Gemini Live
- SIP/Fale Paco + Asterisk + AudioSocket
- PostgreSQL/Supabase as durable source of truth
- Redis for hot cache/state
- RabbitMQ for operational queues/events
- Tool Registry for all external actions
- WhatsApp fallback/continuity
- No price negotiation
- No audio recording in MVP

## Realtime rule

Redis, RabbitMQ, PostgreSQL and n8n must never carry PCM/audio frames.

```text
Fale Paco SIP -> Asterisk -> AudioSocket -> Go Voice Engine -> Gemini Live WSS
```

## Engineering

```text
PRD -> SDD/ADR -> Microtask -> TDD RED -> Implement -> GREEN -> Refactor -> Evidence -> Review
```

Agents: Hermes, OpenClaw, Antigravity, Cursor. Reviewer: Anorak.

See `AGENTS.md` and `docs/`.
