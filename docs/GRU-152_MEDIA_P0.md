# GRU-152 / PR #137 — C media source P0

Reviewed baseline: `a7580529866cdf3a07059c915d8f22806b8f816c`.

## Reproduced defect

The old shared object's actual ausrc callback fails when 18 valid framed
records are already queued. Its greedy reader fills 16387 bytes before
parsing one frame and returns EMSGSIZE (errno 90). The deterministic no-call
harness reproduced **0 decoded frames and one audio-source error** for 17280
PCM bytes / 17334 stream bytes. `test-legacy-parser.sh` repeats this RED gate
against that exact reviewed Git source, modifying only an obsolete header
enum workaround for the installed libre2 headers.

Inspection of upstream Baresip **v1.1.0** `src/audio.c:ausrc_error_handler`
and `src/call.c:audio_error_handler` confirms that a source error propagates
to `UA_EVENT_AUDIO_ERROR`, stops the call streams, and emits CALL_EVENT_CLOSED.
Source: <https://github.com/baresip/baresip/tree/v1.1.0> (downloaded source
archive checksum is pinned in CI). This proves a reproducible local failure
mechanism capable of closing a call. It does **not** recover the missing
AUDIO_ERROR/SIP trace of the prior owner call or prove its historical ordering.

## Implementation

- `stream_source.c` implements header/payload state, exact missing-byte reads,
  960-byte payload validation, and one complete record per tick. It handles
  fragmented/coalesced records, EINTR, EAGAIN, orderly close, invalid type and
  invalid length. User-space buffering is at most **963 bytes**, independent
  of producer burst size. Remaining audio backpressures in bounded socket/Go
  queues; no arbitrary speech frames are dropped.
- Production ausrc uses monotonic 20ms deadlines. Partial/absent records
  produce silence without losing framing state. Missed ticks never trigger a
  catch-up burst. Error callbacks are followed by no access to released source
  state. Existing player, codecs, account and control profile remain unchanged.
- `gru151_media_stats` exposes fixed source counters and safe error classes.
- ctrl_tcp normalizes AUDIO_ERROR to a fixed class, and CallService captures
  the first correlated error and first Gemini/audio/terminal timestamps without
  storing arbitrary event parameters, changing terminal classification, or
  invoking Hangup. Diagnostic slots are process-local; canonical lifecycle
  and transcript persistence are unchanged.

## No-call evidence

All local checks below passed:

| Gate | Result |
|---|---|
| Old actual shared object + 18-frame burst | RED reproduced: errno 90, frames=0, errors=1 |
| Shared production C parser | Single, 1+1+1/2+1 header, partial payload, 2/18 coalesced, partial next, EINTR/EAGAIN, invalid type/length, EOF PASS |
| Actual production `.so` via ausrc allocator | 1/2/18/250 records PASS, order preserved, zero errors/corruption |
| Go PCM24 → real adapter → Unix → actual `.so` | 18 and 250 frame bursts PASS, normal and race builds |
| Cadence and underrun | Approximately 340ms between first/last of 18 frames; 4980ms for 250, paced callbacks; zeroed silence frames |
| Source counters | frames_received=18/250, protocol_errors=0, socket_errors=0, max_buffered_bytes=963, last_error_class=none |
| Installed Baresip 1.1.0 no-provider smoke | Empty accounts; actual `.so` registers ausrc/auplay and returns safe counter command; no INVITE |
| AddressSanitizer + UndefinedBehaviorSanitizer parser | PASS |
| Real no-call JEV/Gemini fixture | JEV once, directive sent, Gemini 466 SLIN24 frames, turn_complete=yes, roles lead/agent, agent persisted once, readback=yes |
| Go full suite / race / vet / build / diff | PASS |
| OpenAPI | Root and embedded specs synchronized; contract tests PASS |

The real JEV/Gemini fixture uses a labeled synthetic lead FINAL and real
providers; its TX consumer is a Go socket fixture. The separate actual-module
Go/C gate exercises the real C source and cadence, preventing an adapter-only
fixture from concealing a C regression. PCM is not written to disk.

CI adds a **C Media Module** job that builds the production `.so`, exercises
the real ausrc allocator, reproduces baseline RED, runs Go→actual-C normal/race
stress, and checks the parser with sanitizers. It installs development headers
only in the CI runner and never upgrades the owner's Baresip.

## Review boundary

Owner real calls remain **3**; Codex calls **0**. No fourth call, Dial, INVITE,
retry, SIP credential/profile mutation, VPS access, Asterisk or FreeSWITCH.
Return to Anorak. `READY_FOR_OWNER_CALL_4=no`.

These checks use the newly built module in an isolated fixture; the existing
owner-facing API/Baresip process has not been restarted as part of this gate.
Reload the corrected runtime only as the next reviewed local preflight step,
before any owner retest. Do not infer a live-call E2E PASS from these fixtures.
