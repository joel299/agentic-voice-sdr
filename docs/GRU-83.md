# GRU-83 — SIP HTTP API → canonical Asterisk runtime

Status: In Review / PR #48

## Ownership

- `internal/httpapi/**` owns the HTTP DTO and explicit mapping.
- `internal/telephony/sip/**` is the canonical SIP operational contract from GRU-82.
- GRU-83 does not create a second `TrunkConfig`, Manager, reconciler, or PJSIP generator.

## Runtime flow

```text
HTTP /v1/config/sip-trunk
  → SIPConfigRequest.Validate
  → SIPConfigRequest.ToCanonical
  → sip.TrunkConfig
  → sip.Manager.ApplyTrunk
  → RealAsteriskReloader
  → Asterisk
```

`ASTERISK_PJSIP_CONFIG_DIR` enables the operational composition. The configured path must exist and be a directory. Without it, or when construction is invalid, the router uses a fail-closed boundary and returns `501 SIP operational boundary unavailable`; it does not report a false success. The HTTP composition uses a resolver-aware SIP dialer that rejects private/loopback/link-local destinations both during initial resolution and again at dial time. Before calling the canonical Manager, the production boundary validates every enabled destination and stores validated network addresses separately from logical SIP identities. `Host`, `Registrar`, and `OutboundProxy` remain logical values for SIP identity; their pinned addresses are used for operational transport/configuration. Mixed DNS answers are rejected, eliminating Asterisk-side hostname DNS rebinding. TLS preflight uses the pinned address with the original logical host as ServerName. Disable requests skip external destination resolution and reach `Manager.RemovePJSIPConfig` using only local trunk-name validation.

## Security

The HTTP response uses `SIPSafeResponse` and omits `auth.secret`. Secrets are passed transiently into the canonical manager only, and are not logged, traced, persisted, or written to tracking evidence. The canonical manager and reloader retain ownership of PJSIP file staging, reload, verification, commit and rollback.

## Provider limitation

No external WhatsApp provider contract is changed by GRU-83. No provider endpoint or credential is invented.

## Verification

The GRU-83 tests cover explicit field mapping, `userpass`/`ip`/`none` auth mapping, enabled/disabled forwarding, manager failure masking, HTTP-to-manager invocation, configured canonical composition, and missing-runtime fail-closed behavior.
