# GRU-87 — Gemini Live realtime session boundary

## Execution evidence

- Environment: `LOCAL`
- Remote VPS/SSH: `NOT USED`
- Official reference: Google AI for Developers, [raw WebSockets](https://ai.google.dev/gemini-api/docs/live-api/get-started-websocket), retrieved during implementation.
- Endpoint contract: `wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=...`
- Configurable model: `GEMINI_LIVE_MODEL`; documented default at implementation time: `gemini-3.8-live`.
- Input contract: raw PCM16 mono little-endian at 16 kHz, sent as `audio/pcm;rate=16000`.
- Output contract: decoded raw PCM audio event, expected `audio/pcm;rate=24000`.

## Local TDD gate

`go test -count=1 ./...`: PASS

`go test -race -count=1 ./...`: PASS

`go vet ./...`: PASS

`go build ./...`: PASS

`git diff --check`: PASS

The local fake WebSocket contract covers setup ordering/acknowledgement, text, PCM audio, output audio, transcription, interruption, tool-call parsing without execution, malformed/unknown protocol handling, cancellation, secret redaction, concurrent writer ownership, and graceful close.

## Real gate

`GEMINI_API_KEY` was checked for presence only and is absent in the LOCAL environment.

`HUMAN_GATE: GEMINI_API_KEY absent in LOCAL environment`

No remote lookup, SSH, VPS access, secret retrieval, or fabricated real PASS was performed. The real Gemini Live WSS smoke test remains blocked until the key is made available locally.
