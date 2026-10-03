# GRU-152 / PR #137 — conversation latency and owner-selected Fola

## Scope and provenance

Repository `joel299/agentic-voice-sdr`, Linear project **Agentic Voice SDR**,
GRU-152, existing PR #137 and its existing branch. Starting reviewed HEAD:
`871efdfbaa942cdf8bc3edee7f77534e24580e62`.
Linear issue/latest comments, selected Shared Agent Memory account ending
`5052f`, its Source of Truth document, and GitHub PR were consulted before edits.
No new task, architecture, provider, telephone call, redial, Asterisk,
FreeSWITCH, VPS change, or SIP credential/profile edit.

Owner confirmed the latest call below is the conversation estimated at four
minutes. Its measured connected duration is **122.538945 seconds**:

| Field | Value |
|---|---|
| API call | `call_3cf5b89024fafe29d29a43c853f9e00b` |
| Baresip call | `2be0b0b824032896` |
| Created UTC | `2026-10-03T00:29:29.690822Z` |
| Connected UTC | `2026-10-03T00:29:37.592805Z` |
| Ended UTC | `2026-10-03T00:31:40.131750Z` |
| Final lead / agent turns | 8 / 6 |
| Actual binary | `/tmp/gru152-api-68ad344` |
| Embedded actual revision | `68ad344a6b1939bbe7c0cd608cf094e67b7e4c80` |

**That call did not run the reviewed 871efdf HEAD.** Database status was failed
with stale running AI state, consistent with the earlier binary's lifecycle.
This report does not rewrite that historical evidence.

## Historical measured baseline: seven usable turns

Only FINAL-to-first-Gemini-audio and subsequent Go admission clocks were
retained. Physical speech end, C callback, and outbound RTP clocks were absent.
Do not interpret the following as speech-end-to-user-heard-audio latency.
The eighth FINAL overlapped the previous response/call shutdown and has no
complete first-audio measurement.

Statistics use the median and nearest-rank p95 (seven samples: p95 is maximum).

| Measured boundary (ms) | Min | p50 | p95 / max |
|---|---:|---:|---:|
| FINAL → first Gemini audio | 1306.80 | 1768.00 | 2745.07 |
| JEV decision | 295.42 | 345.29 | 969.42 |
| Controlled send → Gemini first audio | 961.65 | 1400.06 | 1765.90 |
| Gemini first audio → Go write/admission proxy | 0.48 | 0.92 | 2.00 |
| FINAL → JEV start | 2.86 | 4.80 | 6.87 |
| JEV complete → controlled send | 2.22 | 2.89 | 4.15 |

Gemini accounts for **73.62% of the sum of measured FINAL-to-first-audio
latencies**; this is a sum-of-stages/sum-of-totals calculation, not division of
medians. No historical JEV timeout was observed among those seven decisions.
There is no measured 100–300 ms local scheduling hole to remove.

## Implemented boundaries

- A call-scoped collector retains at most 256 turn traces, deduplicates TurnID,
  and records first-observed stage clocks only. It records JEV payload bytes,
  connection reuse, DNS/connect/TLS/first-byte/request timing without content,
  credentials, URLs, or provider errors.
- Lead physical speech end is not invented. Runtime PCM energy produces a
  **labeled estimate** (`speech_end_basis=pcm_energy_estimate`), with derived
  metrics prefixed `estimated_`. Synthetic fixtures have known end clocks and
  `fixture_known_end`; only these can prove the no-call engineering target.
- Gemini output begins forwarding on EventAudio, before generationComplete,
  turnComplete, or the final agent transcript. GenerationComplete still does
  not release the active response lease. PCM remains mono S16LE, RX 16 kHz /
  20 ms and TX 24 kHz / 20 ms at the C source.
- Go first-write timing is taken at the successful Unix write boundary, not
  queue admission. An optional fixed 32-byte reverse metadata ACK associates
  the real C source callback with the first PCM sequence of a turn. Partial
  ACKs are reassembled; timing congestion loses metadata, never PCM. Old
  modules simply leave C clocks unavailable. RTP is not inferred from a C ACK.
- Historical call metadata also showed RX high-water 32, 51 dropped frames,
  TX high-water 32 and 48,442 ms *cumulative* TX backpressure. That wait is not
  48 seconds of response latency. The API now explicitly uses RX four frames
  (80 ms), TX two frames (40 ms), and requests a 1024-byte Unix send buffer
  instead of an unbounded OS-default backlog. Linux enforces its own minimum;
  requested buffer bytes are not a claim of exact kernel PCM latency.
  The reusable adapter's defaults and explicitly configured test capacities
  remain compatible. Actual 18/250-frame source tests with this API budget
  preserve every sample/order under backpressure; the smaller RX budget must
  continue reporting drops during owner retests rather than hiding pressure.
- A single bounded response receive owner (four events; provider messages
  already have a finite size limit) can cancel an in-flight contextual PCM
  write on `interrupted`. It clears partial samples, the Go queue, and queued
  C IPC bytes via a private 0600 datagram control socket and explicit ACK at
  the next paced tick. Epochs prevent late metadata from an older turn from
  being attributed to new PCM. SIP and media sockets remain open.
- Actual C fixture proves old samples are absent after the flush ACK and new
  PCM is emitted. Cancellation is bounded; failure to flush is explicit and
  uses the existing degraded-AI handling, not a SIP hangup.
- **Limit:** independent input and controlled-response sessions do not share
  automatic VAD interruption. This patch proves the controlled session's
  `interrupted` handling, not full lead-triggered barge-in across sessions.
  No unauthorized early JEV turn or invented provider cancellation was added.

## Official protocol research

[Live capabilities](https://ai.google.dev/gemini-api/docs/live-api/capabilities),
[Live API schema](https://ai.google.dev/api/live),
[Live best practices](https://ai.google.dev/gemini-api/docs/live-api/best-practices),
[voice design](https://ai.google.dev/gemini-api/docs/voice-design), and
[speech generation](https://ai.google.dev/gemini-api/docs/speech-generation)
were consulted. Model remains `gemini-3.8-live`; its setup does not accept
thinkingLevel, so none was added. Input is incremental 20 ms PCM, not a large
utterance buffer. Tools and phone input remain excluded from the controlled
response session. The existing commercial prompt/rules remain intact.

## VAD A/B — synthetic speech, no telephone

Five generated PCM fixtures: short, normal, 350 ms natural pause, “sim”, and a
roughly one-second internal pause. They were paced as 640-byte/20 ms mono
PCM16 frames. Input-only instruction was held constant across this isolated
VAD experiment; it was shorter than the sales prompt used by the full runtime.
The keyword score is **keyword coverage**, not a full WER or a subjective
phonetic clipping assessment. Additional leading PCM protects the synthetic
boundary. Generation transcripts confirmed the expected fixture phrases.

| Variant | Simple-fixture FINAL delays (ms) | Internal-pause / completion evidence |
|---|---|---|
| Current automatic default | 1164, 1162, 1160, 1249 | Early FINAL; second clause absent |
| Explicit 500 ms / LOW end sensitivity | 848, 841, 837, 915 | Early FINAL; second clause absent |
| Explicit 800 ms / LOW | 1130, 1122, 1132, 1195 | Early FINAL; second clause absent |
| Explicit 1200 ms / LOW | 1522, 1519, 1531, 1589 | No early FINAL, but only 50% keyword coverage |
| Hybrid local 1200 ms + audioStreamEnd | No FINAL in any fixture | Failed, including extra flush wait |

All four simple fixtures had full keyword coverage with automatic variants.
None of the tested alternatives passed **all** pause/completion criteria.
The faster 500 ms setting therefore **was not enabled**. Runtime retains
provider-default automatic VAD, with no explicit silenceDurationMs and no
hybrid/audioStreamEnd activation. The opt-in experiment exists for review and
further calibration. It is not a claimed successful optimization.

## JEV connection and payload experiment

Existing `http.DefaultTransport` already reuses keep-alive connections across
client construction. No extra per-turn TLS handshake was found. A real 20 + 20
request A/B reduced representative payloads from 1966 to 1521 bytes (22.6%)
without changing canonical criteria or the five tested intent classifications.

| JEV A/B | Successful requests | p50 (ms) | p95 (ms) | Reused |
|---|---:|---:|---:|---:|
| Original | 20/20 | 290.14 | 323.86 | 19/20 |
| Compact experiment | 20/20 | 297.92 | 334.06 | 20/20 |

The first original request measured DNS 35.54 ms, TCP 36.89 ms, TLS 60.42 ms;
warm requests had zero new DNS/TCP/TLS work. Compact instructions did **not**
improve this sample and remain disabled in production. Timeout remains
1500 ms; it was not reduced to disguise latency or increase failures.

## Provider benchmark: 20 JEV + 20 Gemini + 5 text pipelines

These experiments use persistent controlled sessions and the active canonical
prompt. They exclude VAD, C pacing and RTP; text-pipeline values are not the
primary end-to-end metric. No hidden retry or telephone control was used.

| Experiment / boundary | Successes | p50 (ms) | p95 (ms) |
|---|---:|---:|---:|
| Before JEV | 20/20 | 306.50 | 410 |
| Before Kore first audio | 20/20 | 1125.00 | 1536 |
| Before text JEV→Gemini | 5/5 | 1578 | 1898 |
| After JEV | 18/20 | 404.00 | 923 |
| After Fola/style first audio | 20/20 | 1203.50 | 1720 |
| After text JEV→Gemini | 5/5 | 1558 | 1763 |

Two candidate JEV requests exhausted 1500/1501 ms; the failed samples are
retained, not silently retried or counted as successes. Timing distributions
above include successful observations only. Candidate text pipeline changed
p50 by +1.27% and p95 by +7.11% improvement, but first-Gemini-audio regressed.
This is insufficient evidence of a repeatable latency gain. Benchmark tests
now explicitly fail incomplete provider gates after saving metadata.

## Real synthetic audio → existing split runtime → actual C source

A separate test uses both real Gemini sessions, JEV, FinalTranscriptHandler,
TurnRuntime, ResponseGate, SplitBridge, bounded Go IPC and the loaded production
C module. It stops after first C callback; it is a first-audio measurement, not
full telephone/RTP or complete call lifecycle evidence.

An initial run obtained four C observations, then failed Gemini response
connection; evidence is retained separately. A subsequent explicitly recorded
A/B attempted five before and five after fixtures, with no per-fixture retry:
4/5 before and 4/5 after produced C observations. One after observation had a
premature FINAL during the internal pause and is excluded from valid
speech-end comparisons. Two attempts had no FINAL; the instrumentation did
not manufacture a duration. Thus the five-successful-fixture gate is **FAIL**.

| Valid speech-end → C emission | Before (4) | After (3) |
|---|---:|---:|
| p50 (ms) | 2846.24 | 2979.88 |
| p95 (ms) | 3841.31 | 3132.75 |
| VAD / transcription p50 (ms) | 1228.36 | 1221.05 |
| JEV p50 (ms) | 327.29 | 327.63 |
| Gemini first audio p50 (ms) | 1286.29 | 1291.12 |
| Gemini audio → Go wire p50 (ms) | 0.03 | 0.03 |
| Go wire → C emission p50 (ms) | 9.12 | 9.90 |

Different missing fixtures make the all-success distributions unpaired.
The three **common valid fixtures** are normal, natural_pause and yes:

| Paired metric | Before | After | Improvement |
|---|---:|---:|---:|
| Speech-end → C p50 (ms) | 2789.26 | 2979.88 | **−6.83%** |
| Speech-end → C p95 (ms) | 2903.22 | 3132.75 | **−7.91%** |

Do not claim reduced conversation latency. Valid after media-added latency is
under 21 ms. Approximately 1.22 s transcription finalization plus 0.33 s JEV
plus 1.29 s Gemini already exceeds the 1 s median target before media work.
These medians describe components and are not an exact additive distribution.

| Engineering gate | Result |
|---|---|
| JEV p50 ≤300 ms | Not consistently met; isolated original A/B passed |
| Added media latency ≤100 ms | Initial valid A/B passed; final small-buffer sample has a 170 ms outlier, so not a consistent PASS |
| Speech-end→first media p50 ≤1000 ms | FAIL |
| Speech-end→first media p95 ≤1500 ms | FAIL |
| Five successful full audio fixtures per variant | FAIL |
| Native Fola accepted and emitted PCM | PASS |

## Final explicit small-buffer probe

A further five-fixture candidate experiment used the actual API RX=4 / TX=2
and limited Unix buffer, preserving previous attempts. Four valid fixtures
produced C callbacks, with **zero RX drops**. The fifth failed Gemini response
setup; no retry was made. The real-provider test correctly returned FAIL for
that incomplete five-fixture gate.

| Final candidate (four valid fixtures) | p50 (ms) | p95 / max (ms) |
|---|---:|---:|
| Speech end → C emission | 2804.25 | 3122.48 |
| Speech end → FINAL | 1218.34 | 1258.98 |
| JEV | 299.50 | 567.57 |
| Gemini first audio | 1288.86 | 1326.83 |
| Gemini first audio → C emission | 18.94 | 170.04 |

Relative to the earlier four-valid-fixture baseline, median changed by +1.48%
and p95 by +18.71%, but small samples/network variability and session resets
prevent attributing a repeatable gain to buffer tuning. The stronger claimed
result is the smaller explicit queue budget and preserved paced sample output.
JEV median met 300 ms in this sample; the overall speech targets did not.
The isolated 167.30 ms Gemini→Go-write observation was not reproduced in a
separate, explicitly recorded one-fixture instrumentation probe: that probe
observed a 6240-byte first audio chunk, 0.035 ms Gemini→Go and 1.866 ms Go→C.
It does **not** prove the earlier outlier's cause. Future traces retain first
chunk byte count and provider-receive clocks to distinguish partial-frame
assembly, queued events and scheduling. No zero padding or pacing removal
was introduced to make the numbers look favorable.

## Voice quality and codec boundary

Owner requested **Fola**. The official authenticated voice catalog returned
`en-us-fola` / Fola; `voiceName=Fola` was accepted by Live and produced native
24 kHz WAV. Kore, Fola and Aoede were tested through the existing protected
no-call agent-turn endpoint, with the same model, active prompt, lead intent,
short-phrase instruction, style and PCM rate. Free generation varied the
spoken text/duration; this is not a guaranteed identical-waveform/text test.
The owner judges timbre; Codex does not label a voice “better” by opinion.

Fola is now the API default. VoiceDescription/VoiceStyle already enter the
frozen system instruction; they are not inert stored values. Style requests
one or two short PT-BR sentences and one question, preserving commercial rules.
`<breath>` is included as a subtle natural-breath style instruction that must
not be spoken. It is an officially documented **TTS** tag, but a dedicated
Live API breath control was not confirmed; no TTS integration or fabricated
Live field was added.

Native WAV comparisons are private in `.runtime/voice-samples/` (0600):
`voice-Kore.wav`, `voice-Fola.wav`, `voice-Aoede.wav`.
A separate Fola native→PCMU/8000→PCM24k comparison is clearly named
`fola-simulated-phone-boundary.wav`. This is a simulated narrowband boundary,
**not** the actual last call or proof of its negotiated codec.

Last-call negotiated SIP codec, RTP rate and ptime are **unknown**: no retained
SDP/PCAP/recording exists, Baresip output was intentionally discarded to avoid
SIP secrets, and the closed call cannot be queried. The 16k/24k custom-module
rates describe local PCM, not the negotiated RTP codec. Native and simulated
samples help compare timbre/bandwidth, but cannot attribute that call's poor
voice quality conclusively to Gemini versus telephony.

## Recording: no original call audio exists

Only authorized private runtime/source/generated-profile directories were
searched for WAV/MP3/OGG/FLAC/PCAP artifacts. None existed for the call.
`last_call_recording_found=no; reason=live_audio_was_not_persisted`.
The synthetic voice samples are not a reconstructed or original recording.

Future diagnostic recording is **disabled by default**. Explicit local
configuration requires both `OWNER_TEST_RECORDING=true` and
`OWNER_TEST_RECORDING_DESTINATION` matching the intended owner-test destination;
other authorized calls run normally without recording. Owner auth/allowlist
remain enforced. No provider recordings or new recording HTTP endpoints.

Opt-in writes private separate `lead.wav` (16 kHz) and `agent.wav` (24 kHz)
under `.runtime/owner-recordings/owner-test-*` (0700 directory, 0600 files).
The bounded asynchronous queue defaults to 32 frames and drops with explicit
completeness counters instead of blocking media. Each stream is capped at ten
minutes. No PCM goes into Git, PostgreSQL, memory or logs. Retention cleanup
runs before a new recording and removes only diagnostic directories older
than 24 hours. **Delete the private directory manually after analysis**;
shutdown alone is not an automatic expiry scheduler.

These are ingress and generated/admitted-egress streams, not an aligned mixed
replay or proof of which samples the remote caller heard. Dropped chunks,
interrupted queued audio and missing wall-clock alignment mean they must not
be presented as a complete mixed call. No extra realtime mixer was introduced.

## Reproduction and review

Real-provider tests require explicit opt-in output paths and protected local
credentials; they skip in ordinary unit/CI runs. They never connect to ctrl_tcp
or use Dial. Preserve failed attempts; don't loop until a favorable sample.

```sh
GRU152_BENCHMARK_OUTPUT="$PWD/.runtime/latency.json" go test -v -count=1 -run '^TestRealNoCallLatencyBenchmark$' ./cmd/api
GRU152_VAD_OUTPUT="$PWD/.runtime/vad.json" go test -v -count=1 -run '^TestRealNoCallVADBenchmark$' ./cmd/api
GRU152_JEV_AB_OUTPUT="$PWD/.runtime/jev-ab.json" go test -v -count=1 -run '^TestRealJEVCompactAB$' ./cmd/api
GRU152_FULL_PIPELINE_OUTPUT="$PWD/.runtime/pipeline.json" BARESIP_C_TEST_BIN=/absolute/module-source-test BARESIP_C_TEST_MODULE=/absolute/gru151_media.so go test -v -count=1 -run '^TestRealNoCallFullPipelineFirstAudio$' ./cmd/api
```

Actual module gates retain 1/2/18/250 frames, fragmented headers/payloads,
coalescing, EINTR/EAGAIN, 963-byte parser bound and 20 ms realtime pacing.
Race and ASan/UBSan include the production .so plus the flush fixture.
Provider performance gates above remain separate from unit/CI correctness.
Keep GRU-152 **In Progress**, PR #137 open, no merge and no automatic call.
