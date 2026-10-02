# GRU-152 — local outbound allowlist and restart gate

Correction of PR #137, baseline `4d1836f5dbce47b5250b42178a6945fca222da4f`.
No live call, Dial, POST /v1/calls against the real runtime, or INVITE is part of this gate.

## Pre-restart evidence

Inspected API PID 1609774, binary `/tmp/gru152-api-4d1836f`, embedded baseline revision and clean build. One API and one child Baresip; Tailnet :8443 forwards to this API on loopback :8080.

| Check | Observed |
| --- | --- |
| Running allowlist present | yes |
| Running raw count | 1 |
| Running normalized count | 1 |
| Running allowlist contains authorized destination | yes |
| Protected project .env allowlist present | yes |
| .env has exactly the authorized destination | yes |
| .env permission | 0600 |
| Real config.Load -> NewAllowlist reconstruction authorizes destination | yes |
| Registration / active-call no-call smoke | REGISTERED / empty |

The owner reported the earlier 403 at approximately 13:00 local / 17:00 UTC. The owner subsequently confirmed the Tailnet Scalar URL, `https://joelquintana.tail212bac.ts.net:8443/docs`; the exact HTTP request and historical policy snapshot were not retained. The inspected process environment, file and real parser do **not** demonstrate any of A (restart did not load env), B (file regressed), or C (parser bug). The historical 403 source remains unconfirmed. Reconstructing the startup policy from `/proc` is not a live heap inspection of the old service. We do not claim a historical root cause from an unconfirmed hypothesis.

## Implemented behavior

- Empty policy: `ErrDestinationPolicyNotConfigured`; HTTP 503 `outbound call policy is not configured`, before any provider request.
- Configured policy with another number: existing `ErrDestinationDenied` / HTTP 403.
- `AllowedDestinations()` snapshots the same immutable policy used by `Start`; normalized/deduplicated entries are returned in deterministic order with no mutable map exposure.
- Owner-protected `/v1/runtime/status`: `outbound_call_allowlist_configured`, `outbound_call_allowed_count`, `outbound_call_allowed_destinations`, `outbound_call_ready`.
- `/readyz`: 503 unless policy, API, ctrl_tcp, registration, active prompt, JEV and Gemini configuration are ready. Public output contains readiness only; allowed numbers stay owner-only.

## Canonical local launch

Build the reviewed head, then use only:

```sh
scripts/run-local-api.sh /absolute/path/to/the/reviewed-api-binary
```

The wrapper determines the worktree root, disables shell tracing and execs that exact binary. The binary's `--local-env .env` path reads/exports the protected local file, validates all required names, normalizes with CallService, then re-execs the same binary with this environment. The final process's `/proc` startup environment and Config.Load therefore agree. The preflight prints names as present/missing and a normalized count; it never prints credential values. Required keys missing from the file are cleared instead of inheriting stale interactive values.

File syntax is literal `KEY=value`, optional `export ` prefix, blank lines/comments and matching outer single/double quotes. No shell evaluation, variable expansion, command substitution or escape interpretation is performed. Literal `$` in credential values is preserved. Unsafe permissions, symlinks, file replacement during opening, duplicate assignments, invalid names/quotes and oversized lines/files are rejected with fixed errors.

Minimum required keys: GEMINI_API_KEY, GEMINI_LIVE_MODEL, OPENROUTER_API_KEY, OPENROUTER_JEV_MODEL, BARESIP_CTRL_TCP_ADDR, BARESIP_PROFILE_DIR, BARESIP_MEDIA_MODULE_PATH, OUTBOUND_CALL_DESTINATION_ALLOWLIST, PGHOST, PGPORT, PGUSER, PGDATABASE. This owner-local launch requires exactly one unique normalized destination; the protected file contains only `+5567981340687`.

The no-provider preflight can be run without starting any runtime:

```sh
/absolute/path/to/the/reviewed-api-binary --check-local-env .env
```

## Regression gates

Deterministic tests cover empty-policy 503/readiness false, canonical/formatted authorization, deduplication/immutable snapshots, different destination denied without Dial, Config.Load restart fixture, required file keys not inherited, unsafe file/symlink rejection and owner-only status. Policy tests use fake providers or policy-only normalization. Readiness checks never dial.

Run Go tests and race tests, vet/build/diff/OpenAPI checks, the unchanged actual C module tests (1/2/18/250 records), Go-to-actual-C stress and exact-head CI. An initial C harness run under simultaneous Go compile/test load failed its timing assertion at an 8ms minimum gap; isolated rerun passed (18/250 frames minimum 19ms), with zero protocol/socket/source errors. The C source/test logic was not changed.

Safe runtime acceptance: corrected embedded head, one API, one Baresip, owner status showing one allowed destination and readiness true, REGISTERED, empty call inventory, same source account bytes/mtime/0600, and protected Tailnet status on `https://joelquintana.tail212bac.ts.net:8443`. Preserve C media P0 and all SIP credentials. No fourth call; no merge, Done, VPS, Asterisk or FreeSWITCH. Return evidence to Anorak. `READY_FOR_OWNER_CALL_4=no`.
