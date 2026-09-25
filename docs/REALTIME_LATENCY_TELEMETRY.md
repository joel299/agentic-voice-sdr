# Realtime voice latency telemetry foundation (GRU-103)

`internal/telemetry` is a provider-neutral, in-memory measurement boundary. It
accepts only a canonical metric name and monotonic-capable `time.Time` start/end
values and stores only a duration in milliseconds. It does not export, persist,
log, or accept PCM, transcripts, credentials, or other call payloads.

## Measurement boundaries

Callers capture timestamps with `time.Now()` at the following lifecycle points
and call `Timer.Finish` or `Recorder.Record` at the corresponding end point.
These definitions are elapsed durations, not wall-clock timestamps:

| Metric | Start | End |
|---|---|---|
| `gemini_connect_ms` | Gemini session connection attempt starts | Session reports ready/connected |
| `setup_ack_ms` | Realtime session setup request is sent | Matching setup acknowledgement is received |
| `first_input_transcription_ms` | First input audio for a turn is committed/sent | First corresponding input transcription is received |
| `first_output_transcription_ms` | Turn is submitted for model response | First output transcription for that response is received |
| `first_audio_ms` | Turn is submitted for model response | First output audio chunk for that response is emitted |
| `turn_latency_ms` | Lead speech end is detected | First agent audio for the response is emitted |
| `interruption_latency_ms` | User interruption is detected | Current agent audio playback is stopped |
| `tool_call_detect_ms` | Provider tool-call signal is received | Application recognizes the tool-call event (not tool execution) |

These boundaries are instrumentation contracts for future realtime hooks. This
package does not add Gemini, AudioSocket, SIP, or application behavior. If a
caller has no reliable event for one endpoint, it must omit that observation,
not substitute a guessed timestamp. Use timestamps from the same process where
possible so Go's monotonic clock component is retained.

## Hot-path and overload contract

`Recorder.Record` performs only bounded metric validation, duration arithmetic,
a non-blocking channel send, and an atomic drop counter update. It never waits
for a consumer and performs no filesystem, database, broker, or network I/O. A
full queue drops the observation rather than applying backpressure; consumers
must inspect `Dropped()` to understand data loss. `Drain()` is intended for a
non-realtime consumer and may allocate, so it must not run in the PCM loop.
Queue capacity is caller-configured and in-memory only; no worker or external
exporter is started here.

Consumers may later aggregate samples into P50, P95, and P99, provided they
preserve the metric definitions and account for dropped samples. No aggregation,
storage, dashboard, or exporter is included in this foundation.

## Engineering-only latency references

Values such as voice-turn P50 < 900 ms, P95 < 1.5 s, Gemini first audio < 600
ms, and Go internal overhead < 20 ms are engineering assumptions/targets for
future validation only. They are not measured results, an SLA, or a product
promise. This package defines no thresholds and does not claim these targets
are currently met.
