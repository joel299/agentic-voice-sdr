# GRU-152 / PR #137 — P0 response regression after latency tuning

## Scope

Same repository `joel299/agentic-voice-sdr`, Linear project Agentic Voice SDR,
issue GRU-152, PR #137 and its existing branch. Linear/latest comments, selected
Shared Memory account ending 5052f (context/search), Source of Truth document,
GitHub PR and actual local binary were consulted before code edits.

Baseline `871efdfbaa942cdf8bc3edee7f77534e24580e62`; candidate
`89c777263a552ecb8b10611885562fa563445abf`; exactly one intervening commit.
The temporary detached baseline worktree contains unchanged tracked sources.
No telephone call, Dial, INVITE, retry/redial, VPS, SIP credential, VAD/model,
JEV timeout/strategy, recording activation or architecture change.
Latency/voice experiments are paused pending Anorak review.

## Latest owner attempt and evidence boundary

| Field | Observed value |
|---|---|
| API call | `call_b7e3fdc7759155540869b30541a86b51` |
| Provider call | `2dfb406e08fe3d4c` |
| Actual binary | `/tmp/gru152-api-89c7772` |
| Embedded revision | `89c777263a552ecb8b10611885562fa563445abf`, modified=false |
| Created UTC | 2026-10-03T16:12:24.784921Z |
| Ended UTC | 2026-10-03T16:13:25.423771Z |
| Attempt elapsed | 60.638850 seconds |
| connected_at / connected duration | NULL / unavailable |
| Lifecycle status | failed |
| AI status / stage / failure class | stopped / gemini_response_receive / NULL |
| Lead FINAL / agent FINAL persisted | 0 / 0 |

Owner explicitly confirmed **he answered and spoke** at 12h12 Campo Grande.
Nevertheless, logs/database retained CALL_PROGRESS and CALL_CLOSED, **no
CALL_ESTABLISHED or connected_at**. This mismatch remains unresolved; do not
fabricate a SIP 200/ACK or connected duration, or dismiss the owner's account.

Timeline UTC:
- 16:12:27.658749 media session open.
- 16:12:27.678436 input connect started; ready 16:12:28.524294.
- 16:12:28.526881 response connect started; ready 16:12:29.246745.
- 16:12:29.256075 bridge run.
- 16:12:29.258201 first media ingress; 16:12:29.258874 successful input audio send.
- 16:12:29.273106 response session resumption metadata.
- 16:13:25.423127 provider CALL_CLOSED; bridge teardown follows.

No input FINAL, JEV request, directive, controlled send, output transcription,
Gemini audio, generationComplete, turnComplete, Go real-agent PCM or real C
frame was observed. No degraded mode or Gemini transport failure was recorded.
The persisted response-receive stage reflects the session-resumption event,
**not proof a controlled business response was requested**.

Call counters: RX dropped=76, RX high-water=4; TX high-water/waits/real frames=0.
C emitted 2887 silence callbacks, received=0, protocol/socket errors=0.
Ctrl dropped events=0. The missing FINAL places the failure before JEV.
The actual voiced contents and timing of discarded RX were not retained.

## A/B before correction — same credentials/providers/prompt/machine

The no-call probes load the same protected literal environment and active
PostgreSQL Prompt V1, model gemini-3.8-live, immutable core and Fola/style.
No secrets, transcript text, provider payload or PCM are printed/persisted.

| Test | 89c777 | 871efdf |
|---|---|---|
| Isolated fixed controlled directive | PASS: setup/send/output text/audio/generationComplete/turnComplete | PASS: all six gates |
| Real JEV → controlled response | PASS | PASS |
| Real input PCM → FINAL → JEV → split bridge → paced Go TX | PASS: 1 FINAL, 1 JEV, 36 audio events, turnComplete | PASS: 1 FINAL, 1 JEV, 34 audio events, turnComplete |

Therefore **an unconditional isolated-Gemini failure was not reproduced**.
Fola itself is not established as a cause. Kore with the old style also passed
isolated and JEV tests on 89c777. The original receiver/session can speak when
intelligible input reaches it; no provider outage is assumed to excuse the
specific media regression below.

## Reproduced regression: early speech lost before consumer starts

Real API media ingestion starts before sequential Gemini setup is complete.
Adapter.readRX keeps a bounded queue and drops the oldest frame when full.
Reducing capacity from 32 frames (640ms) to four (80ms) can erase a short early
utterance while setup delays the consumer.

An additional no-call fixture sends a real synthetic “sim” (416.3125ms) and
120ms trailing silence into RX **before starting the bridge consumer**. It
then streams silence for automatic VAD, with default VAD unchanged.

| Startup replay | Candidate RX=4 | Baseline defaults RX=32 | Same candidate, only restored defaults |
|---|---:|---:|---:|
| Consumer delay ms | 541 | 542 | 540 |
| Queue high-water | 4 | 27 | 27 |
| RX drops | 23 | 0 | 0 |
| Lead FINAL | 0 | 1 | 1 |
| JEV decisions | 0 | 1 | 1 |
| Gemini audio events | 0 | 23 | 25 |
| TurnComplete | **FAIL** | **PASS** | **PASS** |

No per-attempt retries. Candidate FAIL was retained through its 40s context
deadline; it received only trailing silence, not the early voiced phrase.

A deterministic unit reproduction holds TX at 32 and varies **only RX**.
It sends 20 voiced 20ms frames followed by seven silence frames before the
consumer starts:
- RX=4 drops 23, retaining four silence frames and **zero voiced frames**.
- RX=32 drops zero, retaining all 20 voiced frames plus silence in exact order.
This proves the queue mechanism independently of Gemini/network variation.

**Cause proved:** the 89c777 RX reduction introduces startup speech loss and
can suppress the entire FINAL→JEV→Gemini chain.
**Historical attribution limit:** the owner's actual waveform was not captured.
76 drops and absent FINAL are compatible with this mechanism, but do not prove
that all his later speech was lost this way. The missing established event
also remains an unresolved observation. Do not label either inference as
confirmed root cause of every second of that call.

## Narrow production correction

- Restore `baresipmedia.Config{}` from 871efdf through
  `localBaresipMediaConfig`: bounded 32 RX / 32 TX and normal Unix buffer.
  Remove the production RX=4, TX=2, socket=1024 experimental overrides.
  This is the sole functional tuning rollback; no return to 68ad344.
- Preserve 871efdf overlapping FINAL serialization, pending FINAL, single
  response lease, transcript idempotency and AI/media isolation.
- Retain Fola, style, receiver, passive timing and C parser/source implementation
  after no-call gates. No further voice or performance tuning.
- Add explicit guards around interrupted trace marks. The reviewed
  `TurnTrace.Mark` already checks nil, and the no-active-trace regression test
  passed before the guards: **no nil panic proved**, no EventInterrupted in
  the owner's timeline. This is defensive clarity, not the claimed call cause.
- Add versioned opt-in isolated/real-JEV and startup full-bridge provider tests.
  They skip without explicit output-path flags, retain failures, never retry,
  never use telephony control, and write metadata only.
- Extend the actual-C fixture with `production` config selection, preventing
  a successful test of a different media budget from being called an API gate.

## Group isolation

| Group | Evidence / outcome |
|---|---|
| Voice | Fola and Kore/old style both pass isolated + JEV; Fola retained |
| Buffers | Startup RX4 FAIL; same 89c777 defaults PASS; deterministic RX-only reproduction |
| Receiver | Baseline synchronous and current asynchronous bridges pass; 100 audio + 3 lifecycle events, slow PCM writer, exact order/no loss and deterministic cleanup |
| Interruption/flush | No interrupted event or response request in historical call; real C 1/2/18/250 runs without flags and provider-C probe with timing/flush flags pass; existing blocked-write interruption and actual flush tests pass |
| Telemetry | Startup FAIL reproduced without collector; same defaults PASS without collector; actual-C provider probe with collector PASS; slow-writer event/lifecycle tests with collector on/off match |
| Recording | Disabled in diagnostics and live runtime; no recording hook activated or new call audio saved |

These tests narrow the reproduced failure to RX startup preservation.
They are not an exhaustive proof of every possible interruption/network race.

## Corrected functional gates

- Versioned real startup regression: PASS, one FINAL, one JEV, 18 audio events,
  six output-transcription events, turnComplete, 323 observed TX
  frames before cancellation; admitted frames=332, RX drops=0, high-water=27.
  TX wait duration 4066ms is cumulative pacing/backpressure, not a single delay.
- Versioned isolated and real-JEV controlled tests: PASS setup/send/output text/
  audio/generationComplete/turnComplete, Fola, Prompt V1, no retry.
- Actual real input→JEV→Gemini→Go Unix→loaded production C: PASS first real C
  callback and no source error. Stops at first callback, so this C test alone
  is not proof of the whole remote call or outbound RTP; complete turn is
  proved separately by the full-bridge gate.
- Actual C stress: 1/2/18/250 frames, parser fragmentation/coalescing,
  EINTR/EAGAIN, bounded 963-byte parser, 20ms pacing, no corruption/error.
- Race and ASan/UBSan include the actual module/source and parser; existing
  interruption flush tests remain regression coverage.

## Reproduction (no telephone)

Protected .env and the private synthetic fixture are required for paid
provider tests. Run each explicitly; a failure is retained, not redialed:

```sh
GRU152_P0_CONTROLLED_OUTPUT="$PWD/.runtime/p0-controlled.json" go test -v -count=1 -run '^TestRealNoCallControlledResponseRegression$' ./cmd/api
GRU152_P0_REGRESSION_OUTPUT="$PWD/.runtime/p0-startup.json" go test -v -count=1 -run '^TestRealNoCallStartupResponseRegression$' ./cmd/api
GRU152_FULL_PIPELINE_OUTPUT="$PWD/.runtime/p0-c.json" GRU152_FULL_PIPELINE_VARIANT=production GRU152_FULL_PIPELINE_FIXTURE=yes BARESIP_C_TEST_BIN=/absolute/module-source-test BARESIP_C_TEST_MODULE=/absolute/gru151_media.so go test -v -count=1 -run '^TestRealNoCallFullPipelineFirstAudio$' ./cmd/api
go test -v -count=1 -run '^TestRXStartupBudgetPreservesSpeechBeforeConsumer$' ./internal/telephony/baresipmedia
```

The initial A/B used unchanged source heads and common no-call probe code;
the candidate-only restored-default replay kept model/voice/receiver/recording/
telemetry unchanged. Metadata artifacts live under ignored private .runtime;
no waveform belongs in Git, Linear or Shared Memory.

## Rollout/review boundary

After full Go/race/vet/build/diff gates and exact-head CI, restart only the
local known API when registered and no call is active. Preserve .env and
source/generated account bytes; verify binary revision and registration.
Record final HEAD/CI/runtime in the tracking handoff. Keep issue In Progress,
PR open, no merge. **READY_FOR_OWNER_RETEST=no**, return to Anorak; do not
resume latency tuning or execute a telephone call automatically.

