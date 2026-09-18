# TDD v1.0 — Test Strategy

## Mandatory loop
SPEC -> TEST -> RED -> IMPLEMENT -> GREEN -> REFACTOR -> REGRESSION -> EVIDENCE -> REVIEW.

## Levels
- Unit: domain rules/state machines/retry/tool validation.
- Contract: Gemini/AudioSocket/RabbitMQ/Redis/provider schemas.
- Integration: Postgres/Redis/RabbitMQ/API/Tool Registry.
- E2E: campaign -> call -> scheduling/WhatsApp -> transcript/memory.

## Gates
No Done with failing tests, missing acceptance coverage, regression, scope expansion or missing evidence.

Use `go test -race ./...` where concurrency is relevant.
