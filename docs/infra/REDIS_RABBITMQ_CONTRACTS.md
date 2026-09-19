# Low-Latency Infrastructure Contracts: Redis & RabbitMQ

**Issue:** GRU-57 — Define Redis + RabbitMQ low-latency infrastructure
**Architectural Baseline:** ADR-002, SDD v1.0, PRD v1.0
**Owner:** Antigravity / Arquimedes
**Reviewer:** Anorak
**Status:** In Review

---

## 1. Executive Summary & Purpose

This specification defines the formal infrastructure contracts for **Redis** (ephemeral hot cache, state coordinator, rate limiting, and deduplication) and **RabbitMQ** (asynchronous operational broker, task queue, and event fan-out) for the **Agentic Voice SDR** platform.

It operationalizes **ADR-002** into immutable contracts, eliminating architectural ambiguity before the functional implementation of Go adapters, domain state machine handlers, and external integrations.

---

## 2. Hard Architectural Rules & Non-Negotiable Boundaries

### Rule 2.1 — PostgreSQL is Canonical Source of Truth
- **PostgreSQL** is the sole authoritative system of record for all business entities, historical call records, lead qualification state, and audit logs.
- **Redis is strictly non-authoritative**. All data stored in Redis must be disposable and reconstitutable from PostgreSQL or live external events. Under no circumstances may Redis be treated as persistent storage.

### Rule 2.2 — Realtime Zero-PCM Path Invariant & Latency Objective
- **The realtime audio processing path is strictly:**
  $$\text{Fale Paco SIP} \longleftrightarrow \text{Asterisk} \longleftrightarrow \text{AudioSocket (TCP linear PCM)} \longleftrightarrow \text{Go Voice Engine} \longleftrightarrow \text{Gemini Live (BiDi WebSocket)}$$
- **Absolute Architectural Invariant:** **Redis, RabbitMQ, PostgreSQL, and n8n MUST NEVER enter the PCM/audio frame path.**
- **Latency Objective:** The system targets an end-to-end conversational round-trip latency objective under 800ms. The architecture explicitly acknowledges that external SIP trunk transit, carrier routing, Asterisk jitter buffers, public network variance, and Gemini Live inference contribute to the total end-to-end latency. The Go Voice Engine minimizes internal processing overhead by processing raw 20ms linear PCM chunks (160 samples @ 8kHz or 320 samples @ 16kHz, 16-bit mono) in lock-free memory loops without synchronous database, broker, or disk I/O.

### Rule 2.3 — Zero Dual-Write Invariant
- Direct dual-writing from application code to both PostgreSQL and RabbitMQ within the same business transaction is strictly forbidden.
- Asynchronous messaging across domain boundaries MUST use the **Transactional Outbox Pattern** committed atomically within the PostgreSQL transaction.

### Rule 2.4 — Zero Hardcoded Secrets
- All credentials (passwords, usernames, tokens) MUST be provided strictly via environment variables or secret managers.
- Configuration files (`rabbitmq.conf`, `definitions.json`, Docker Compose, application manifests) and repository commits must NEVER contain default, fallback, or hardcoded passwords or password hashes.

---

## 3. Redis Low-Latency Infrastructure Contract

### 3.1 Key Naming Conventions & Namespaces

Redis keys use colon `:` as a hierarchical delimiter with the mandatory root namespace prefix `agentic:`.

$$\text{Format: } \texttt{agentic:\{domain\}:\{entity\_id\}:\{purpose/sub-entity\}}$$

| Namespace Pattern | Type | Canonical Purpose | Example |
| :--- | :--- | :--- | :--- |
| `agentic:call:{call_id}:session` | Hash | Active call volatile state machine cache (telephony ID, current status, active node, timing) | `agentic:call:c4a8-92f1:session` |
| `agentic:lead:{lead_id}:hot` | String (JSON) | Hot cached lead profile, CRM attributes, qualification tags, past contact timestamps | `agentic:lead:ld-7890:hot` |
| `agentic:lock:{resource}` | String | Distributed mutex lock for critical atomic operations | `agentic:lock:call:c4a8-92f1` |
| `agentic:lease:{resource}` | String | Distributed worker node lease / channel heartbeat assignment | `agentic:lease:worker:node-01` |
| `agentic:dedup:{event_id}` | String | Event deduplication key for idempotency enforcement | `agentic:dedup:evt-550e8400` |
| `agentic:idempotency:{idempotency_key}` | String (JSON) | API request fast-path idempotency cache with response envelope | `agentic:idempotency:req-99ab-12` |
| `agentic:ratelimit:phone:{e164}` | Sorted Set / Hash | Sliding window dial rate limit counter per destination phone number | `agentic:ratelimit:phone:+5511999998888` |
| `agentic:ratelimit:campaign:{campaign_id}` | String (Counter) | Token bucket / CPS (calls per second) limiter per campaign | `agentic:ratelimit:campaign:cmp-winter-26` |
| `agentic:tool:{tool_call_id}:result` | String (JSON) | Transient tool execution result cache for conversation context replay | `agentic:tool:tc-01j8k9:result` |

### 3.2 Comprehensive TTL Matrix

Every key written to Redis **MUST have an explicit TTL** assigned at creation time. Keys without TTL are prohibited to prevent memory leaks and unevicted state drift.

| Key Pattern | Data Structure | TTL | Eviction Category | Justification |
| :--- | :--- | :--- | :--- | :--- |
| `agentic:call:{call_id}:session` | Hash | **1 hour** (auto-refreshed on state change) | Volatile state | Retains state during call duration. Auto-expires if call terminates abruptly without cleanup. |
| `agentic:lead:{lead_id}:hot` | String (JSON) | **15 minutes** | Read cache | Bounds eventual consistency window with PostgreSQL. Invalidated explicitly on lead update. |
| `agentic:lock:{resource}` | String (UUID) | **5 to 30 seconds** | Lock | Short-lived lease to prevent deadlock if worker crashes. Released via `defer` or Lua script. |
| `agentic:lease:{resource}` | String (NodeID) | **60 seconds** | Lease | Worker lease. Heartbeat renews TTL every 15 seconds (`TTL / 4`). |
| `agentic:dedup:{event_id}` | String ("1") | **24 hours** | Deduplication | Guarantees at-least-once consumers drop duplicate deliveries within the replay window. |
| `agentic:idempotency:{key}` | String (JSON) | **24 hours** | Idempotency | Ensures duplicate external API invocations receive identical responses within standard idempotency window. |
| `agentic:ratelimit:phone:{e164}` | Sorted Set | **24 hours** (or configured window) | Policy limit | Configurable dialing rate limit window per telephone number. |
| `agentic:ratelimit:campaign:{id}`| String | **1 second / 1 minute** | Throttle | Enforces maximum concurrency and Calls Per Second (CPS) per campaign. |
| `agentic:tool:{id}:result` | String (JSON) | **30 minutes** | Tool Cache | Retains function execution results during conversation turn. |

### 3.3 Cache-Aside Pattern, Miss Behavior & Invalidation

#### Read Path (Cache-Aside)
1. Application receives request for entity (e.g. Lead `ld-7890`).
2. Query Redis: `GET agentic:lead:ld-7890:hot`.
3. **Cache Hit:** Parse JSON and return entity immediately (< 2ms).
4. **Cache Miss:**
   - Acquire short-lived mutex `agentic:lock:lead:ld-7890:hot` (TTL 3s) to prevent **Cache Stampede**.
   - Query canonical PostgreSQL database.
   - If record exists: serialize to JSON, write to Redis with `SET agentic:lead:ld-7890:hot <json> EX 900`, and return.
   - If record does **not** exist in PostgreSQL: write a **Sentinel Tombstone** `SET agentic:lead:ld-7890:hot "NULL" EX 60` to protect against **Cache Penetration** / denial-of-service, then return 404/NotFound.
   - Release mutex.

#### Invalidation Rules (Write-Around)
1. Business mutation executes in PostgreSQL:
   ```sql
   BEGIN;
   UPDATE leads SET status = 'QUALIFIED', updated_at = NOW() WHERE id = 'ld-7890';
   INSERT INTO outbox_events (...) VALUES (...);
   COMMIT;
   ```
2. **Invalidation:** Do NOT update Redis directly in the API handler before commit.
3. The Outbox event consumer (or post-commit hook) executes an explicit cache eviction:
   ```redis
   DEL agentic:lead:ld-7890:hot
   ```
4. Subsequent reads naturally re-populate fresh state from PostgreSQL via the Cache-Aside read path.

### 3.4 Memory Management & Eviction Policy

- **Maxmemory:**
  - Dev/Local: `256mb`
  - Staging/Production: Dimensioned based on concurrent calls ($N \times 50\text{KB} + \text{buffer}$, e.g. 2GB for 10,000 concurrent calls).
- **Eviction Policy:** `allkeys-lru`
  - **Rationale:** Because all keys are assigned explicit TTLs and PostgreSQL is the canonical source of truth, under unexpected memory pressure Redis evicts the least recently used keys gracefully without data corruption. Cold lead caches and stale sessions are evicted first; active locks and active call sessions are accessed frequently and remain hot.
- **Persistence:**
  - Disabled (`--save ""` and `--appendonly no`). Redis is an in-memory cache/coordinator. Any cold restart recovers state seamlessly from PostgreSQL and active AudioSocket sessions.

### 3.5 Distributed Locks & Leases Contract

#### Lock Acquisition
Must be strictly atomic using `SET key token NX PX <ttl_ms>`:
```
SET agentic:lock:call:c4a8-92f1 "550e8400-e29b-41d4-a716-446655440000" NX PX 10000
```
- Returns `OK` if acquired; returns `nil` if locked.
- The `token` MUST be a cryptographically random UUIDv4 unique to the caller goroutine.

#### Safe Lock Release (Lua Script)
Releasing a lock must verify token ownership to prevent deleting a lock acquired by another process after a timeout:
```lua
-- KEYS[1]: lock key (e.g. agentic:lock:call:c4a8-92f1)
-- ARGV[1]: caller token (UUID)
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end
```

#### Fencing Tokens
For writes to shared external state, locks must return a monotonically increasing fencing counter (stored in Redis or derived from PostgreSQL sequence) that downstream storage validates to reject out-of-order writes from delayed lock holders.

### 3.6 Rate Limiting Contract

Rate limiting uses the **Sliding Window Log** algorithm via Redis Sorted Sets (`ZSET`).
*Note on Compliance & Policy:* Maximum dial attempt limits per destination phone number and campaign windows are configurable application parameters. The platform enforces flexible sliding-window rate limits, with specific compliance rules and regulatory policy definitions evaluated and approved at the pre-production compliance review gate.

```lua
-- KEYS[1]: agentic:ratelimit:phone:+5511999998888
-- ARGV[1]: current_timestamp_ms
-- ARGV[2]: window_size_ms (e.g. 86400000 for 24 hours)
-- ARGV[3]: max_allowed_limit (e.g. configurable limit per campaign/policy)
-- ARGV[4]: unique_entry_id (UUID)

local window_start = ARGV[1] - ARGV[2]
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", window_start)
local current_count = redis.call("ZCARD", KEYS[1])

if current_count < tonumber(ARGV[3]) then
    redis.call("ZADD", KEYS[1], ARGV[1], ARGV[4])
    redis.call("PEXPIRE", KEYS[1], ARGV[2])
    return 1 -- Allowed
else
    return 0 -- Rejected (Rate limit exceeded)
end
```

### 3.7 Idempotency Fast-Path Contract

API endpoints accepting an `Idempotency-Key` header follow this deterministic state machine:

```
[Incoming Request with Idempotency-Key: K]
               │
               ▼
   GET agentic:idempotency:K
   ├── Found (status: "COMPLETED") ──► Return cached status code & body immediately (Fast Path)
   ├── Found (status: "IN_PROGRESS") ─► Return 409 Conflict / 425 Too Early (Request concurrently running)
   └── Not Found ─────────────────────► SET agentic:idempotency:K '{"status":"IN_PROGRESS"}' NX EX 300
                                               │
                                               ▼
                                      Execute Domain Transaction (PostgreSQL)
                                               │
                                               ▼
                                      SET agentic:idempotency:K '{"status":"COMPLETED","body":...}' XX EX 86400
```

### 3.8 Health & Readiness Probes

- **Liveness Probe:**
  - Command: `redis-cli ping`
  - Expected response: `PONG` within 100ms.
- **Readiness Probe:**
  - Command: Script verifying `PING` + `INFO memory` (`used_memory < maxmemory * 0.95`) and client connections (`connected_clients < maxclients * 0.9`).

---

## 4. RabbitMQ Topology & Messaging Contract

### 4.1 Topology Overview & Retry Architecture

RabbitMQ operates as a reliable, asynchronous message broker for operational commands, lifecycle domain events, external tool execution, transcript persistence, and stage-based delay retries.

```
                         ┌──────────────────────────────────────────────────────────┐
                         │                     RabbitMQ Broker                      │
                         │                                                          │
                         │   [voice.commands] Exchange (topic, durable)             │
                         │      ├── call.dispatch ────────► (call.dispatch) Queue   │
                         │      │                                                   │
                         │      │  [Stage-Based Delay Queues (No active consumers)] │
                         │      ├── call.retry.30s ───────► (call.retry.30s) Queue  │
                         │      │                             TTL 30s ──► DLX: voice.commands (call.dispatch)
                         │      ├── call.retry.120s ──────► (call.retry.120s) Queue │
                         │      │                             TTL 120s ─► DLX: voice.commands (call.dispatch)
                         │      ├── call.retry.600s ──────► (call.retry.600s) Queue │
                         │      │                             TTL 600s ─► DLX: voice.commands (call.dispatch)
                         │      │                                                   │
                         │      └── tool.job.# ───────────► (tool.jobs) Queue       │
                         │                                                          │
                         │   [voice.events] Exchange (topic, durable)               │
                         │      └── call.transcript.# ────► (transcript.persist)    │
                         │                                                          │
                         │   [voice.dlx] Exchange (topic, durable)                 │
                         │      └── voice.dead / # ───────► (voice.dead) Queue [DLQ]│
                         └──────────────────────────────────────────────────────────┘
```

### 4.2 Exchanges Specification

| Exchange Name | Type | Durability | Auto-Delete | Routing Key Convention | Purpose |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `voice.commands` | `topic` | **Durable** | `false` | `call.dispatch`, `call.retry.*`, `tool.job.*` | Imperative commands dispatched to voice nodes, retry delay queues, and tool workers. |
| `voice.events` | `topic` | **Durable** | `false` | `call.event.*`, `call.transcript.*` | Domain lifecycle events published by call session state machines. |
| `voice.dlx` | `topic` | **Durable** | `false` | `#`, `voice.dead` | Dead Letter Exchange receiving poison messages and exhausted retries. |

### 4.3 Queues Specification

#### Category A: Active Work Queues
Configured with Dead Lettering to `voice.dlx` (`voice.dead`) for unhandled failures, exhausted retries, or poison pills.

| Queue Name | Durable | DLX | DLQ Routing Key | Target Consumer | Prefetch (QoS) |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `call.dispatch` | `true` | `voice.dlx` | `voice.dead` | Outbound Dialer / Telephony Originator | **10** |
| `tool.jobs` | `true` | `voice.dlx` | `voice.dead` | Tool Execution Worker (Composio, CRM, WhatsApp) | **20** |
| `transcript.persist`| `true` | `voice.dlx` | `voice.dead` | Transcript & Summary Storage Worker | **50** |
| `voice.dead` | `true` | *None* | *None* | Poison / Failure Inspection & Operational Alerting | **1** |

#### Category B: Stage-Based Delay Queues (Deterministic Retries)
These queues **do not have active consumers**. They hold messages for a fixed TTL duration, after which RabbitMQ dead-letters them back to `voice.commands` with routing key `call.dispatch`.

| Queue Name | Durable | Fixed Queue TTL (`x-message-ttl`) | Dead Letter Exchange (`x-dead-letter-exchange`) | Dead Letter Routing Key (`x-dead-letter-routing-key`) |
| :--- | :--- | :--- | :--- | :--- |
| `call.retry.30s` | `true` | **30,000 ms (30s)** | `voice.commands` | `call.dispatch` |
| `call.retry.120s` | `true` | **120,000 ms (120s)** | `voice.commands` | `call.dispatch` |
| `call.retry.600s` | `true` | **600,000 ms (600s)** | `voice.commands` | `call.dispatch` |

### 4.4 Bindings & Routing Matrix

| Exchange | Routing Key | Destination Queue | Queue Behavior |
| :--- | :--- | :--- | :--- |
| `voice.commands` | `call.dispatch` | `call.dispatch` | Immediate processing by dialer workers. |
| `voice.commands` | `call.retry.30s` | `call.retry.30s` | Holds message for 30s -> auto-dead-letters to `call.dispatch`. |
| `voice.commands` | `call.retry.120s`| `call.retry.120s`| Holds message for 120s -> auto-dead-letters to `call.dispatch`. |
| `voice.commands` | `call.retry.600s`| `call.retry.600s`| Holds message for 600s -> auto-dead-letters to `call.dispatch`. |
| `voice.commands` | `tool.job.#` | `tool.jobs` | Consumed by tool execution workers. |
| `voice.events` | `call.transcript.#` | `transcript.persist` | Consumed by transcript persistence workers. |
| `voice.dlx` | `#` (or `voice.dead`) | `voice.dead` | Dead Letter Queue for poison / exhausted messages. |

### 4.5 Standard AMQP Message Envelope & Headers

All messages published to RabbitMQ MUST use the following envelope structure:

#### AMQP Message Properties
- `content_type`: `application/json`
- `content_encoding`: `utf-8`
- `delivery_mode`: `2` (Persistent)
- `message_id`: UUIDv4
- `correlation_id`: UUIDv4 (traces message chain across services)
- `timestamp`: UNIX timestamp UTC
- `app_id`: `agentic-voice-sdr`

#### AMQP Message Headers (`application_headers`)
```json
{
  "traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
  "tracestate": "rojo=1",
  "x-correlation-id": "99283110-b06f-42aa-b3f3-816522420b55",
  "x-call-id": "c4a892f1-4389-4e02-9e23-772189bb1234",
  "x-lead-id": "ld-789012",
  "x-idempotency-key": "idemp-call-dispatch-c4a892f1-attempt-1",
  "x-retry-count": 0,
  "x-max-retries": 3,
  "x-first-failed-at": null,
  "x-last-error": null
}
```

#### Payload Schema (Example: `call.dispatch`)
```json
{
  "event_id": "550e8400-e29b-41d4-a716-446655440000",
  "call_id": "c4a892f1-4389-4e02-9e23-772189bb1234",
  "lead_id": "ld-789012",
  "campaign_id": "cmp-q3-cold-saas",
  "phone_e164": "+5511999998888",
  "prompt_version": "v1.4",
  "voice_model": "<configured Gemini Live model>",
  "metadata": {
    "lead_name": "Dr. Carlos Silva",
    "clinic_name": "Odonto Excellence",
    "timezone": "America/Sao_Paulo"
  }
}
```

### 4.6 Publisher Confirms & Reliability Guarantees

1. **Publisher Confirms Required:** Every publisher MUST initialize its AMQP channel with `confirm.select`.
2. **Synchronous/Batch Acknowledgement:**
   - The publisher awaits an affirmative Ack from the broker.
   - If a Nack or publish timeout (5000ms) occurs, the transaction MUST NOT be considered published, and the Outbox worker must reschedule the message.

### 4.7 Consumer Acknowledgement & QoS Contract

1. **Manual Acknowledgement Only:** `auto_ack = false` is mandatory across all consumers.
2. **Prefetch Limits:** Every consumer MUST configure `basic.qos(prefetch_count, global=false)` prior to consuming.
3. **Acknowledgement Policy:**
   - **`basic.ack`**: Issued only after local processing (and associated DB update) successfully commits.
   - **`basic.nack(requeue=false)`**: Issued when a message fails permanently, is malformed, or exceeds retry limits, routing it to `voice.dlx` (`voice.dead`).
   - **Prohibition:** Unconditional `basic.nack(requeue=true)` is strictly forbidden because it triggers instant spinning loops and CPU starvation on unrecoverable errors.

---

## 5. Retry Policy, Backoff Strategy & Dead-Letter Mechanics

### 5.1 Rationale for Stage-Based Delay Queues vs. Single Queue Variable TTL

In RabbitMQ Classic Queues, per-message TTL (`expiration`) suffers from **Head-of-Line (HoL) Blocking**: RabbitMQ only evaluates message expiration when a message reaches the head of the queue. If a message with an expiration of 600s sits at the head of a queue, subsequent messages with an expiration of 30s will NOT expire or dead-letter until the 600s message expires or is removed.

To guarantee **deterministic, non-blocking backoff**, the platform strictly prohibits variable-TTL classic queues and instead defines **stage-based delay queues with fixed queue-level TTL** (`x-message-ttl`).

### 5.2 Deterministic Backoff Lifecycle

```
                                [call.dispatch Worker]
                                           │
                        ┌──────────────────┴──────────────────┐
                        ▼                                     ▼
             [Transient Failure]                    [Permanent / Poison Failure]
                        │                                     │
           Is x-retry-count < 3 ?                             │
           ├── YES ──────────────┐                            │
           │                     │                            │
           ▼                     ▼                            ▼
   x-retry-count == 0    x-retry-count == 1    x-retry-count >= 3
   Increment to 1        Increment to 2        (Retries Exhausted)
   Publish to:           Publish to:                  │
   [call.retry.30s]      [call.retry.120s]            │
         │                     │                      │
         │ (wait 30s)          │ (wait 120s)          │
         ▼                     ▼                      ▼
   Queue TTL Expired     Queue TTL Expired      Publish directly to:
         │                     │                [voice.dlx] -> voice.dead
         └──────────┬──────────┘                (or basic.nack(requeue=false))
                    │                                 │
                    ▼                                 ▼
         Dead-Letter Return to:                 Update PostgreSQL:
         Exchange: [voice.commands]             status = 'FAILED_EXHAUSTED'
         Routing Key: call.dispatch             error_reason = 'MAX_RETRIES_EXCEEDED'
                    │
                    ▼
         Message re-enters [call.dispatch]
         ready for immediate execution
```

### 5.3 Stage Schedule & Routing Matrix

| Stage | Trigger Condition | Target Queue | Queue Fixed TTL | DLX Return Exchange | DLX Return Key | Max Delay |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Stage 1** | 1st transient failure (`count = 0 -> 1`) | `call.retry.30s` | **30,000 ms** (30s) | `voice.commands` | `call.dispatch` | 30s |
| **Stage 2** | 2nd transient failure (`count = 1 -> 2`) | `call.retry.120s`| **120,000 ms** (2m) | `voice.commands` | `call.dispatch` | 120s |
| **Stage 3** | 3rd transient failure (`count = 2 -> 3`) | `call.retry.600s`| **600,000 ms** (10m)| `voice.commands` | `call.dispatch` | 600s |
| **Exhausted**| 4th failure (`count >= 3`) | `voice.dead` | *None* | *None* | *None* | Immediate |

### 5.4 Transition to `voice.dead` & Infinite Loop Prevention

1. **Maximum Retries:** Capped strictly at `3` retry attempts.
2. **Loop Prevention Invariant:**
   - The consumer inspects header `x-retry-count`.
   - If `x-retry-count >= x-max-retries`:
     - Do not publish to any retry queue.
     - Update PostgreSQL call state: `status = 'FAILED_EXHAUSTED'`, `error_reason = 'MAX_RETRIES_EXCEEDED'`.
     - Reject message via `basic.nack(requeue=false)`, causing RabbitMQ to automatically route it to `voice.dead` via `voice.dlx` (or publish directly to `voice.dlx` with routing key `voice.dead`).
     - Emit an OpenTelemetry error event and increment alert metric `voice_calls_dead_letter_total`.
3. **Dead-Letter Header Inspection:**
   - Consumers inspect the `x-death` array populated by RabbitMQ on dead-lettering to verify hop count and ensure messages do not cyclically oscillate between queues.

---

## 6. PostgreSQL & Transactional Outbox Contract

### 6.1 Logical DDL Schema

Critical events originating from domain actions must be written to an `outbox_events` table in PostgreSQL in the **same ACID transaction** as the entity mutation.

```sql
CREATE TABLE IF NOT EXISTS outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type VARCHAR(64) NOT NULL,
    aggregate_id VARCHAR(64) NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    exchange VARCHAR(64) NOT NULL,
    routing_key VARCHAR(128) NOT NULL,
    payload JSONB NOT NULL,
    headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key VARCHAR(128) UNIQUE NOT NULL,
    correlation_id UUID NOT NULL,
    trace_id VARCHAR(64),
    status VARCHAR(24) NOT NULL DEFAULT 'PENDING', -- PENDING, PUBLISHED, FAILED
    retry_count INT NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Partial index for zero-latency polling of pending records
CREATE INDEX IF NOT EXISTS idx_outbox_events_pending
ON outbox_events (created_at ASC)
WHERE status = 'PENDING';
```

### 6.2 Atomicity Invariant (No Dual-Write)

```go
// Canonical Transactional Outbox Implementation Contract
func (s *CallService) DispatchCall(ctx context.Context, cmd DispatchCommand) error {
    return s.db.WithTransaction(ctx, func(tx Transaction) error {
        // 1. Mutate Domain Entity
        callSession := domain.NewCallSession(cmd.CallID, cmd.LeadID)
        if err := tx.InsertCallSession(ctx, callSession); err != nil {
            return err
        }

        // 2. Insert Outbox Event Atomically
        outboxEvent := &OutboxEvent{
            AggregateType:  "CallSession",
            AggregateID:    cmd.CallID.String(),
            EventType:      "call.dispatch.requested",
            Exchange:       "voice.commands",
            RoutingKey:     "call.dispatch",
            Payload:        cmd.ToJSON(),
            IdempotencyKey: fmt.Sprintf("dispatch-%s", cmd.CallID),
            CorrelationID:  telemetry.CorrelationIDFromContext(ctx),
            TraceID:        telemetry.TraceIDFromContext(ctx),
            Status:         "PENDING",
        }
        return tx.InsertOutboxEvent(ctx, outboxEvent)
    })
}
```

### 6.3 Outbox Relay Worker Specification

1. **Polling Mechanism:**
   ```sql
   SELECT id, exchange, routing_key, payload, headers, correlation_id, trace_id
   FROM outbox_events
   WHERE status = 'PENDING'
   ORDER BY created_at ASC
   LIMIT 100
   FOR UPDATE SKIP LOCKED;
   ```
2. **Publishing Loop:**
   - Relay worker reads batch with `FOR UPDATE SKIP LOCKED` (allowing horizontal scaling of multiple relay instances without row contention).
   - Injects trace context into AMQP headers.
   - Publishes each message to RabbitMQ with Publisher Confirms enabled.
3. **Commit & Crash Recovery:**
   - Upon confirming publish:
     ```sql
     UPDATE outbox_events
     SET status = 'PUBLISHED', published_at = NOW()
     WHERE id = :id;
     ```
   - If relay crashes after publishing to RabbitMQ but before database update: the next relay instance will re-publish the message.
   - **At-Least-Once Delivery:** Consumers are guaranteed idempotent via `x-idempotency-key` and Redis deduplication (`agentic:dedup:{idempotency_key}`).

---

## 7. Observability, Distributed Tracing & OpenTelemetry

### 7.1 Distributed Context Propagation

All asynchronous communications across Redis, RabbitMQ, and PostgreSQL must propagate W3C Trace Context headers:

- **AMQP Headers:**
  - `traceparent`: `00-{trace_id}-{span_id}-{trace_flags}`
  - `tracestate`: vendor-specific metadata
  - `x-correlation-id`: global business correlation ID
- **Consumer Span Creation:**
  ```go
  // Consumer Span Lifecycle Contract
  propagator := otel.GetTextMapPropagator()
  parentCtx := propagator.Extract(ctx, amqpHeaderCarrier(delivery.Headers))
  tr := otel.Tracer("voice.consumer")
  ctx, span := tr.Start(parentCtx, "RabbitMQ Consume: "+delivery.RoutingKey,
      trace.WithSpanKind(trace.SpanKindConsumer),
      trace.WithAttributes(
          attribute.String("messaging.system", "rabbitmq"),
          attribute.String("messaging.destination", delivery.Exchange),
          attribute.String("messaging.routing_key", delivery.RoutingKey),
          attribute.String("messaging.message_id", delivery.MessageId),
          attribute.String("messaging.correlation_id", delivery.CorrelationId),
      ),
  )
  defer span.End()
  ```

### 7.2 Core Span Hierarchy

```
[HTTP POST /v1/calls/dispatch] (SpanKindServer)
  │
  ├── [PostgreSQL: BEGIN -> Insert Call -> Insert Outbox -> COMMIT] (SpanKindClient)
  │
  └── [Outbox Relay: Read Pending Events] (Internal)
        │
        └── [RabbitMQ: BasicPublish voice.commands -> call.dispatch] (SpanKindProducer)
              │
              ▼
        [RabbitMQ: BasicConsume call.dispatch] (SpanKindConsumer)
              │
              ├── [Asterisk: Originate Channel via ARI/AMI] (SpanKindClient)
              │
              ├── [AudioSocket: Frame Loop (zero db/queue hops)] (Internal)
              │
              ├── [Gemini Live: BiDi Session Stream] (SpanKindClient)
              │
              └── [RabbitMQ: BasicPublish voice.events -> call.transcript] (SpanKindProducer)
                    │
                    ▼
              [RabbitMQ: BasicConsume transcript.persist] (SpanKindConsumer)
                    │
                    └── [PostgreSQL: Insert CallTranscript] (SpanKindClient)
```

### 7.3 Data Sanitization & PII Redaction in Telemetry

- **Strict Prohibition:** Raw audio frames (PCM), audio byte arrays, base64 speech chunks, and telephony audio buffers must NEVER be attached to tracing spans or log messages.
- **PII Masking:** Destination telephone numbers in span attributes must be masked:
  - Input: `+5511999998888`
  - Span Attribute: `+55119****-8888`
- **Secrets:** API keys, RabbitMQ passwords, and Redis auth tokens must never appear in attributes, URLs, or metadata tags.

---

## 8. Downstream Implementation Guide & GRU-61 Reconciliation

### 8.1 Implementation Verification Checklist
Future engineering issues implementing Redis and RabbitMQ adapters (such as GRU-59, GRU-60, GRU-61, GRU-62) must verify conformance against the following checklist:

- [ ] **No Raw Audio in Brokers:** AudioSocket frame handlers operate purely in-memory; no broker dependencies in the PCM loop.
- [ ] **Redis Non-Authoritative:** If Redis is completely flushed (`FLUSHALL`), the system reconstitutes active state gracefully from PostgreSQL.
- [ ] **Explicit TTL on all Redis Keys:** Every `SET`, `HSET`, `ZADD` is accompanied by an expiration or periodic TTL sweep.
- [ ] **Transactional Outbox:** All events published from domain logic use `outbox_events` inside the domain transaction.
- [ ] **Publisher Confirms:** RabbitMQ client has publisher confirms enabled; publisher handles timeouts and nacks.
- [ ] **Prefetch Configured:** All consumers call `basic.qos` before `basic.consume`.
- [ ] **Manual ACKs:** `auto_ack = false`; failures trigger either stage-based retry routing (`call.retry.30s`, etc.) or reject to `voice.dead`.
- [ ] **Stage-Based Retry Topology:** Retries utilize dedicated fixed-TTL delay queues (`call.retry.30s`, `call.retry.120s`, `call.retry.600s`) dead-lettering back to `voice.commands`/`call.dispatch`.
- [ ] **Safe Dead-Letter Handling:** Messages in `voice.dead` trigger operational alerts and do not auto-requeue without operator intervention.
- [ ] **W3C Trace Context:** `traceparent` is injected on publish and extracted on consume.

### 8.2 Expected Downstream Reconciliation for GRU-61
The current bootstrap dev environment (PR #3 / GRU-61) provisioned a single `call.retry` queue bound with `x-dead-letter-exchange: voice.dlx`. Following formal approval of this GRU-57 specification, GRU-61's declarative definitions in `deploy/dev/rabbitmq/definitions.json` and smoke testing scripts will be reconciled to:
1. Replace the single `call.retry` queue with the three stage-based delay queues: `call.retry.30s`, `call.retry.120s`, and `call.retry.600s`.
2. Configure their fixed queue TTLs (`30000`, `120000`, `600000` ms) and dead-letter arguments pointing to exchange `voice.commands` with routing key `call.dispatch`.
3. Validate deterministic dead-letter return into `call.dispatch` in automated tests.

---

— Arquimedes
