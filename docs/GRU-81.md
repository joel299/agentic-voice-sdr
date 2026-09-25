# GRU-81 — Configuration API boundaries

## Implemented

- WhatsApp configuration, safe readback, instance discovery, selection and connectivity test endpoints under `/v1/config/whatsapp*`.
- `WhatsAppProvider` abstraction with runtime binding through `RuntimeBinding.SetActiveWhatsAppInstance`.
- `HTTPProvider` adapter boundary with injected endpoint paths, bounded JSON responses, timeout, authentication-error masking and normalized instance fields.
- Metadata-only `ConfigStore`, `MemoryConfigStore` and `FileConfigStore`. Persisted fields are provider, base URL, selected instance metadata, provider status and verification timestamp; credentials are never stored.
- SIP HTTP DTOs now live in `internal/httpapi/sip_config.go`. The prior Stark-owned `internal/telephony/sip/config.go` contract was removed to avoid competing with the canonical operational SIP package owned outside this task. GRU-83 owns the later DTO-to-canonical mapping.
- SIP endpoints remain `/v1/config/sip-trunk`; the operational configurator is injected and no Asterisk/PJSIP configuration is generated here.
- `openapi.yaml` documents all seven configuration endpoints, safe responses, errors, write-only credentials, instance states and SIP auth types. The document is suitable for Scalar consumption.

## Security boundaries

- Provider requests enforce a five-second default timeout and a one-megabyte response limit.
- Endpoint validation runs before every request and on every redirect. It rejects localhost, loopback, private, link-local, unspecified, multicast and metadata/internal destinations.
- The transport resolves hostnames immediately before dialing and rejects forbidden resolved addresses, reducing DNS-rebinding risk. Resolver and transport dependencies are injectable for tests.
- Credentials are held only in transient process memory for provider calls. They are excluded from response DTOs, persistence, errors, logs and tracing. No durable plaintext secret storage was introduced.
- `POST /v1/config/whatsapp/test` accepts an empty body because it operates on the already configured provider and active instance; it never sends a customer message.

## External contract limitation

The repository does not contain an authoritative contract for a concrete WhatsApp provider. Therefore the default API wiring intentionally registers no provider adapter and returns `501` until a provider-specific adapter is registered. `HTTPProvider` accepts endpoint paths/functions from that adapter and does not invent provider endpoints. The explicit handoff is: `CONCRETE WHATSAPP PROVIDER CONTRACT REQUIRED`.

The SIP operational boundary is intentionally unavailable in the default router and is handed to GRU-83. This task validates and safely retains the HTTP DTO only.
