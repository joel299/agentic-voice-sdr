# GRU-62 — Stark / Hermes

Linear: https://linear.app/grupoalcate-ia/issue/GRU-62/microtaskcursor-implement-audiosocket-frame-codec-boundary

Owner: Stark / Hermes

Executor handoff: implementation inherited from Cursor and reviewed by Stark.

Implement only the AudioSocket frame codec/parser boundary described in GRU-62.

Mandatory:
- read AGENTS.md and docs/PROMPT_CACHE.md;
- minimal diff;
- TDD first;
- no SIP dialing, Gemini, Redis, RabbitMQ or database changes;
- attach RED/GREEN evidence before Ready for Review.

Scope: `internal/telephony/audiosocket/**` only.

Codec policy:
- wire format is 1 byte type, 2 bytes big-endian payload length, then payload;
- unknown frame types are rejected with `ErrUnknownType`;
- `MaxPayloadSize` is 16384 bytes to bound allocations below the uint16 wire maximum;
- `DecodeReader` uses `io.ReadFull` for partial-read safety.
