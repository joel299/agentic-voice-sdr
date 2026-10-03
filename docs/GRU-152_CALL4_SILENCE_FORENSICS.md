# GRU-152 / PR #137 — call #4 silence forensics

## Canonical identity and governance

Repository `joel299/agentic-voice-sdr`, project **Agentic Voice SDR**, issue
**GRU-152**, PR **137**, existing branch
`joelquintana/gru-152-e2ecodex-api-originated-baresip-jev-gemini-live-call-with-transcript`.
Reviewed starting HEAD: `68ad344a6b1939bbe7c0cd608cf094e67b7e4c80`.

Linear GRU-152 and latest comments, shared PostgreSQL memory (`memory_context`
and `memory_search`), Linear **Shared Agent Memory — Source of Truth**, and the
latest PR comments were consulted before analysis. Execution/status = Linear;
shared context = shared PostgreSQL memory; committed code/history = GitHub.
Local artifacts are supporting evidence, not a replacement canonical memory.

## Exact call and evidence limits

Read-only PostgreSQL query identified the latest real call after the reviewed
API restart. This is not call #3 or the earlier no-call persistence fixture.

| Field | Persisted evidence |
| --- | --- |
| API call ID | `call_6a61ac14d1bea64a84d7f15e0e25916f` |
| Baresip call ID | `a87e5f867d870295` |
| Created | 2026-10-02T23:30:33.215391Z |
| Connected | 2026-10-02T23:30:43.183563Z |
| Ended | 2026-10-02T23:31:08.173867Z |
| Connected duration | 24.990304 seconds |
| SIP terminal status / reason | `failed` / `failed` |
| Stored AI status / stage | `running` / `media_ingress` |
| Stored AI failure / timestamp | absent |
| Final lead transcript turns | 2 |
| Final agent transcript turns / text length | 0 / 0 |

The owner reports hearing Joel's introduction, then silence, and manually
ending the call. No historic BYE trace or reliable remote hangup classification
was retained; owner attribution is the owner report, not an invented SIP proof.
The persisted `failed` terminal status does not prove that our API hung up.

**Zero final agent turns does not prove zero generated speech.** The agent
accumulator persists only at authorized `TurnComplete`; an aborted partial
response is not relabeled as final. Four output-transcription fragments and ten
Gemini AUDIO events were logged. Their text and byte totals were not retained.
The intro's exact text, the full generated partial text, and whether it exceeded
what the owner heard cannot be reconstructed. No raw PCM or provider payload
was saved to fill this gap.

## Call-scoped causal timeline (UTC)

| Timestamp | Evidence |
| --- | --- |
| 23:30:36.994769 | Call-scoped media session opens during SIP progress |
| 23:30:43.183563 | SIP connects |
| 23:30:46.631969 | First FINAL input transcript received |
| 23:30:47.321710 | One JEV invocation completes |
| 23:30:47.325285 | Controlled response sent |
| 23:30:48.672058 | First Gemini AUDIO |
| 23:30:48.672663 | First completed Go media write |
| 23:30:49.105401 | Last of nine completed Go writes logged |
| 23:30:49.274140 | Tenth Gemini AUDIO received; its write does not complete |
| 23:30:50.334224 | Second FINAL arrives while first response is active |
| 23:30:50.691875 | `input_transcription_handler` fails; degraded mode starts |
| 23:31:08.152812 | Media peer tears down |
| 23:31:08.173155 | `CALL_CLOSED`, matching persisted end |

No `GenerationComplete` or `TurnComplete` milestone was observed before the
failure. Unlike those instrumented milestones, historic GoAway/EventClosed were
not instrumented; absence of those logs is **unknown**, not proof of absence.
Input/output connect both succeeded, and JEV and Gemini both ran.

The reviewed handler persists and records the second FINAL, then calls
`Coordinator.Begin`. `ResponseGate.Reserve` rejects a second active cycle before
running JEV. The prior cycle remains active until `TurnComplete`. A deterministic
RED test returns **`response cycle active`** with these same preconditions.
There is one JEV/send in the call logs, no prior completion, successful second
FINAL persistence, and failure at precisely this boundary. The historical raw
error itself was not logged; exact sentinel attribution is supported by these
preconditions plus source analysis and reproduction, not a captured raw error.

`SplitBridge.Run` cancels all three AI loops on that handler error, interrupting
the pending audio write and closing provider sessions. The media transport stays
owned by telephony. `runBaresipMediaSessions` then calls **`ServeDegraded`**, which
feeds silence until owner/provider teardown. This explains the remaining
approximately 17.48 seconds of connected silence without invoking packet loss.

A separate diagnostic bug obscured this: `aiFailureClass` returned
`input_transcription_handler`, which is not an accepted failure-class enum.
CallService rejected that update (log: `ai_runtime_status_persisted=no`). The
stored `running/media_ingress` row is stale and cannot override the failure log.

## Media evidence and attribution

Go counters belong to the new call-scoped session and start at zero internally:

| Counter | Final call-scoped value |
| --- | ---: |
| RX frames dropped | 76 |
| RX queue high water | 32 |
| TX queue high water | 32 |
| TX wait count | 1 |
| TX wait duration | 1066 ms |

These prove pressure, not SIP packet loss. The tenth AUDIO write was pending
when bridge cancellation occurred. No C `AUDIO_ERROR` event was logged.

A **post-call cumulative** read-only `gru151_media_stats` snapshot on the same
Baresip process returned received=974, emitted=1557, starvation-silence=583,
socket-errors=0, protocol-errors=0, max-buffered=963, last-error=`none`.
A measured C baseline was not saved at call start. These totals are explicitly
**not claimed as measured per-call deltas**, nor as a time series proving RTP
reception. C starvation silence also differs from deliberate Go degraded frames;
Go-generated zero PCM counts as a received frame in C. Historic real/degraded
frame counts and the final actual wire-write timestamp are unavailable.

Boundary classification: **overlapping FINAL / active response lifecycle**
causes AI bridge termination and intentional degraded silence. We have not
proved a provider GoAway, reset, C failure, codec failure, or network packet loss.

## Correction

- The single transcript receive owner waits for the current response lease to
  complete before changing ConversationState or starting another JEV/send.
  The next FINAL is persisted once before waiting. The wait creates no worker
  or queue, is canceled by context, and preserves the current lease.
- Response completion, not `GenerationComplete`, releases that wait.
  Stable lead IDs and existing transcript idempotency keys remain unchanged.
- Unknown handler errors persist as accepted `runtime_error`, with the actual
  independent failure stage. Teardown preserves failure stage/class/time.
  Migration 0008 allows explicit `runtime_unknown` stage without inventing a
  runtime shutdown cause. Migration is additive to the vocabulary; no historic
  call row was rewritten and no live migration was applied during forensics.
- Call-scoped safe Gemini diagnostics retain event/audio counts and bytes,
  completion markers, GoAway timeLeft, resumability boolean, close/transport
  class, failure boundary/class and last successful stage. They retain no
  resumption handle, transcript or provider error payload.
- Read-only C source counters now have a measured baseline before HTTP admission
  and a final snapshot for each single-call interval. Resets, missing snapshots
  or another call starting before the final read invalidate the delta. Process
  high-water and last-error gauges are labeled as process values, not deltas.
  This cannot retrospectively create a baseline for call #4.
- Go media now counts **successful Unix-socket writes** separately for real
  agent PCM frames and degraded silence frames, including last real write and
  degraded start timestamps. These counters do not claim remote RTP playback.
- Frame milestones no longer write PostgreSQL for every PCM frame or wait
  behind a database stage update. Queue limits and backpressure remain intact.
- The degraded RX drain is canceled and joined on every exit, including a TX
  error. Raw PCM is never persisted.

## Official Gemini lifecycle check

[Session Management](https://ai.google.dev/gemini-api/docs/live-api/session-management)
and [Live API reference](https://ai.google.dev/api/live) were consulted.
`GoAway.timeLeft` announces remaining connection lifetime, not immediate turn
completion. Session resumption requires setup configuration and server updates;
a handle cannot safely resume state that the server marks non-resumable.
`GenerationComplete` and `TurnComplete` are distinct, including playback timing.

At the reviewed HEAD, GoAway was parsed and forwarded without reconnect; setup
had no `sessionResumption` and retained no resumable handle. This task adds safe
observation, not an unsupported claim that reconnect exists. The owner call's
proven failure is local gate contention, not a demonstrated periodic reset or
GoAway. Resumption/reconnect is therefore **not implemented as this call's fix**.
A synthetic GoAway/close test classifies provider termination separately and
proves it cannot be confused with successful turn completion.

## Reproduction and regression gates (no telephone call)

RED on reviewed code:

- `TestCall4FinalDuringActiveResponseWaitsWithoutKillingRuntime`:
  second FINAL returned `response cycle active` immediately.
- `TestCall4HandlerFailureUsesPersistableClass`:
  returned rejected failure class `input_transcription_handler`.

GREEN and race coverage:

- Overlapping FINAL waits, prior lease stays authorized, cancellation stops
  the waiter, and each FINAL runs JEV exactly once.
- Partial Joel intro + another FINAL + further output + GenerationComplete:
  waiter stays blocked until TurnComplete; one final agent transcript is saved,
  replay creates no duplicate JEV or transcript.
- AUDIO + GenerationComplete + GoAway + close:
  classified `gemini_response_receive`, incomplete turn and safe close metadata.
- Multiple AUDIO chunks + bounded source pressure + TurnComplete:
  all chunks continue, lease completes once.
- AUDIO + unavailable source: classified `media_egress`, not provider close.
- Real and degraded frames/timestamps stay distinct and reset across sessions.
- PostgreSQL 16 contract in a temporary isolated schema accepts handler-failure
  and explicit unknown-stage diagnostics using real migrations.

Real C gates retain 1, 2, 18 and 250 frames; fragmented header/payload,
coalescing, EINTR, EAGAIN, partial frame, actual `.so`, Go -> actual C source,
race and ASan/UBSan. Legacy 18-frame parser failure remains reproduced.
One run under competing compilation failed the unchanged minimum-pacing check
(all 250 frames correct, no corruption/error); isolated rerun passed. The pacing
assertion and C implementation were not weakened.

Full Go tests/race, vet/build, diff check and OpenAPI/embedded Scalar contracts
pass. Exact published HEAD and automated CI run are recorded in the Linear,
GitHub issue and PR handoff, not a self-referential commit hash in this file.

## Review boundary

No new live call, redial, SIP password/profile credential change, `.env` edit,
Asterisk, FreeSWITCH, VPS action, raw audio persistence, merge or Done transition.
The API was not restarted during forensics; reviewed runtime/logs were preserved.
GRU-152 remains **In Progress** and **READY_FOR_OWNER_CALL_5=no**.
