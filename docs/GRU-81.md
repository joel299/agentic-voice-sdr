# GRU-81 — Configuration API boundaries

## Implemented

- WhatsApp configuration, safe readback, instance discovery, selection and connectivity test endpoints under `/v1/config/whatsapp*`.
- `WhatsAppProvider` abstraction with runtime binding through `RuntimeBinding.SetActiveWhatsAppInstance`.
- `HTTPProvider` adapter boundary with injected endpoint URLs, bounded JSON responses, timeout, authentication-error masking and normalized instance fields.
- SIP configuration HTTP boundary under `/v1/config/sip-trunk`; the operational configurator is injected and no Asterisk/pjsip configuration is generated here.

## External contract limitation

The repository does not contain an authoritative contract for a concrete WhatsApp provider. Therefore the default API wiring intentionally registers no provider adapter and returns `501` until a provider-specific adapter is registered. `HTTPProvider` accepts endpoint paths/functions from that adapter and does not invent provider endpoints.

Credentials are held only in transient process memory for provider calls. They are excluded from response DTOs, safe SIP views, errors, logs and tracing. No durable plaintext secret storage was introduced.

The SIP operational boundary is intentionally unavailable in the default router and is owned by GRU-82. This task validates and forwards the structured payload only.
