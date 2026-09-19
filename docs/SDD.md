# SDD v1.0 — System Design

## Stack
Go 1.27.x, Chi v5, REST/OpenAPI/Scalar, WebSocket events, Asterisk/AudioSocket, Gemini Live, PostgreSQL/Supabase, Redis, RabbitMQ, Tool Registry, Composio, WhatsApp API, Docker, OpenTelemetry.

## Realtime Path (Sub-800ms Latency)
$$\text{Fale Paco SIP} \longleftrightarrow \text{Asterisk} \longleftrightarrow \text{AudioSocket} \longleftrightarrow \text{Go Voice Engine} \longleftrightarrow \text{Gemini Live WSS}$$

**Strict Invariant:** No Redis, RabbitMQ, PostgreSQL, or n8n hop is allowed in the PCM audio path.

## Asynchronous Architecture
PostgreSQL domain transaction $\longrightarrow$ Transactional Outbox $\longrightarrow$ Outbox Relay $\longrightarrow$ RabbitMQ $\longrightarrow$ Background Consumers $\longrightarrow$ Redis Invalidation / External APIs.

Formal infrastructure contracts are specified in [docs/infra/REDIS_RABBITMQ_CONTRACTS.md](infra/REDIS_RABBITMQ_CONTRACTS.md).

## Redis Infrastructure Contract
- **Role:** Ephemeral hot cache, distributed lock coordinator, rate limiter, and idempotency fast-path. Strictly non-canonical.
- **Key Hierarchy:** Prefixed with `agentic:{domain}:{entity}:{purpose}` (e.g. `agentic:call:{call_id}:session`, `agentic:lead:{lead_id}:hot`, `agentic:lock:{resource}`, `agentic:dedup:{event_id}`).
- **TTL & Eviction:** Mandatory explicit TTL on every key; `allkeys-lru` eviction policy.
- **Cache-Aside & Invalidation:** Read-through cache-aside with stampede mutex locks; write-around invalidation via Outbox consumer.

## RabbitMQ Topology & Messaging Contract
- **Exchanges (Topic, Durable):**
  - `voice.commands`: Imperative commands (`call.dispatch.*`, `call.retry.*`, `tool.job.*`).
  - `voice.events`: Domain lifecycle events (`call.event.*`, `call.transcript.*`).
  - `voice.dlx`: Dead Letter Exchange for unrecoverable errors (`#`, `voice.dead`).
- **Queues (Durable Classic):**
  - `call.dispatch`: Outbound dial requests and telephony dispatch.
  - `call.retry`: Delayed retry staging queue (Dead-Letter TTL backoff to `call.dispatch`).
  - `tool.jobs`: Async execution of external tools (Composio, CRM, WhatsApp).
  - `transcript.persist`: Async persistence of transcripts and structured call memory.
  - `voice.dead`: Canonical Dead Letter Queue.
- **Quality of Service (QoS):** Manual ACK (`auto_ack=false`) and bounded prefetch per worker type.
- **Retry Mechanics:** Exponential backoff (30s, 120s, 600s) capped at 3 attempts, routing to `voice.dead` upon exhaustion to eliminate infinite loops.

## CallSession Lifecycle
`CREATED` $\longrightarrow$ `DIALING` $\longrightarrow$ `RINGING` $\longrightarrow$ `CONNECTED` $\longrightarrow$ `CONVERSING` $\longrightarrow$ `ENDING` $\longrightarrow$ `COMPLETED`.
Terminal alternatives: `NO_ANSWER`, `BUSY`, `VOICEMAIL`, `FAILED`, `CANCELED`.

## Observability & Distributed Tracing
OpenTelemetry context propagation via W3C Trace Context (`traceparent`) in all AMQP headers and HTTP endpoints. Raw PCM audio and unmasked credentials/PII are strictly excluded from tracing and logs.
