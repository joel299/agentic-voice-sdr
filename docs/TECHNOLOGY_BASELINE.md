# Technology Baseline v0.2 — Low-Latency

- Go 1.27.x
- Chi v5
- REST + WebSocket events
- Asterisk + AudioSocket
- Gemini Live
- PostgreSQL/Supabase source of truth
- Redis hot cache/state
- RabbitMQ operational broker
- Transactional Outbox
- Tool Registry
- Composio
- WhatsApp API
- Docker
- OpenTelemetry

Hard rule: Redis, RabbitMQ, PostgreSQL and n8n stay out of the PCM/audio frame path.
