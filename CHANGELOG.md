# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.1.0-alpha.1] - 2026-09-18

### Added
- **Repository Governance & Documentation (GRU-56)**:
  - Added `CHANGELOG.md`, `docs/PROJECT.md`, `docs/ENGINEERING.md`, `docs/AGENT_PROTOCOL.md`.
  - Added `.github/pull_request_template.md` for standardized agent PR handoffs.
  - Configured repository metadata, branch deletion on merge, and squash merge settings.
- **Redis & RabbitMQ Infrastructure Contract (GRU-57)**:
  - Formalized low-latency caching and message topology specifications in `docs/ADR-002.md`.
  - Documented TTL matrix, eviction policies, lock/lease semantics, rate limiting, and fast-path deduplication for Redis.
  - Defined RabbitMQ exchange/queue bindings (`voice.commands`, `voice.events`, `voice.dlx`), fixed-TTL retry queues (`call.retry.30s`, `call.retry.120s`, `call.retry.600s`), and Dead-Letter Exchange topology.
- **AudioSocket Frame Codec (GRU-59)**:
  - Implemented Go binary protocol parser and encoder for Asterisk AudioSocket frames in `internal/telephony/audiosocket/codec.go`.
  - Support for `UUIDMessage`, `SilenceMessage`, and 16-bit 8kHz PCM linear audio frame chunking.
  - Full unit test suite with 100% frame encoding/decoding assertion coverage.
- **AudioSocket Stream Transport (GRU-60)**:
  - Implemented high-performance TCP stream server and session buffer in `internal/telephony/audiosocket/server.go`.
  - Concurrent connection handler supporting dual 8kHz 16-bit PCM channels (read/write).
  - Clean connection teardown, latency-optimized buffer flushes, and integration tests.
- **Redis & RabbitMQ Development Infrastructure (GRU-61)**:
  - Materialized local dev stack via Docker Compose (`deploy/dev/docker-compose.yml`).
  - Automated RabbitMQ topology auto-loading (`deploy/dev/rabbitmq/definitions.json` and `rabbitmq.conf`).
  - Implemented secretless configuration driven strictly by environment variables without hardcoded fallback credentials.
  - Added automated health check scripts (`scripts/health-check.sh`) and end-to-end retry validation (`scripts/smoke-test.sh`).

### Changed
- Refactored development stack scripts to validate required environment variables before launch.
- Standardized topic routing key bindings for `tool.job.#` and `call.transcript.#` across RabbitMQ definitions.
