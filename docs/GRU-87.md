# GRU-87 — Gemini Live realtime session boundary

## Execution evidence

- Environment: `Stark VPS` (`vultr`), existing branch worktree `/root/work/gru81`.
- Branch: `agent/hermes/gemini-live-session-boundary`.
- PR: #50.
- Official reference: Google AI for Developers, [raw WebSockets](https://ai.google.dev/gemini-api/docs/live-api/get-started-websocket), retrieved during validation.
- Endpoint class: Gemini Live BidiGenerateContent WSS; API key used only in process memory from the authorized runtime document and never logged.
- Production model smoke: `gemini-3.8-live`.
- Input transcription smoke: `gemini-3.5-transcribe-live` with `TEXT` response modality.
- Input contract: raw PCM16 mono little-endian at 16 kHz, sent as `audio/pcm;rate=16000`.
- Output contract: decoded raw PCM audio event, observed `audio/pcm;rate=24000`.

## Real Gemini Live smoke

- WSS authentication/connection: PASS.
- Setup acknowledgement: PASS (`setupComplete`).
- Text input: PASS; real output transcription received.
- PCM16/16 kHz input: PASS; real audio stream accepted by Live API.
- PCM16/24 kHz output: PASS; real audio events received with `audio/pcm;rate=24000`.
- Input transcription: PASS; real interim input transcription events received from the transcription model.
- Output transcription: PASS; real output transcription events received.
- Interruption: PASS; sending real input while model audio was streaming produced `interrupted`.
- Turn complete: PASS; real `turnComplete` event observed in the text/audio smoke.
- Tool-call parsing: PASS; real `toolCall` parsed as `schedule` with arguments; execution was intentionally not performed.
- Raw PCM persisted: `NO`.
- Secrets exposed: `NO`.

## Boundary corrections made

- Moved response modalities into the current `generationConfig` envelope required by the live endpoint.
- Accepted JSON payloads delivered in binary WebSocket frames.
- Added typed function declarations for real tool-call setup/parsing.
- Added configurable response modalities for the transcription smoke.
- Parsed interim and final input transcription event variants.
- Uses `realtimeInput.audioStreamEnd=true` with automatic VAD; `activityEnd` is not sent.

## Regression gates

`go mod download`: PASS

`go test -count=1 ./...`: PASS

`go test -race -count=1 ./...`: PASS

`go vet ./...`: PASS

`go build ./...`: PASS

`git diff --check`: PASS

The local fake WebSocket contract remains green and now also covers the current setup envelope, tool declaration, and interim input transcription parsing. No AudioSocket bridge, resampling, SIP/Asterisk, dispatcher, persistence, Redis, RabbitMQ, n8n, Composio, or WhatsApp work was added.
