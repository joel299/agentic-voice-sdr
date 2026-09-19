# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v0.1.0-alpha.1] — Wave 001

### Added

- **GRU-59 — Go API runtime + health endpoints**
  - Added the Go API runtime and health endpoints.
- **GRU-60 — CallSession state machine**
  - Added the CallSession lifecycle and domain transition contract.
- **GRU-61 — Redis + RabbitMQ development infrastructure**
  - Added the local development stack in `deploy/dev/docker-compose.yml`.
  - Added RabbitMQ topology configuration in `deploy/dev/rabbitmq/definitions.json` and `deploy/dev/rabbitmq/rabbitmq.conf`.
  - Added the infrastructure smoke test at `scripts/infra/smoke-test.sh`.
- **GRU-62 — AudioSocket frame codec**
  - Implemented the AudioSocket frame codec in `internal/telephony/audiosocket/frame.go`.
  - The approved wire format uses one type byte, a two-byte big-endian payload length, and the payload.
  - `0x02` / silence is not supported; it is rejected as an unknown type.

## [v0.1.0-alpha.2] — Wave 002

### Added

- **GRU-63 — AudioSocket stream transport boundary**
  - Added sequential frame transport over `io.Reader` and `io.Writer`, reusing the approved codec.
- **GRU-64 — Retry and business-hours scheduling policy**
  - Added configurable retry scheduling with the official `America/Sao_Paulo` timezone.
- **GRU-66 — AudioSocket TCP server lifecycle**
  - Added the configurable TCP listener and connection lifecycle over the approved stream boundary.
- **GRU-67 — Tool Registry domain contract**
  - Added the Tool Registry domain contract and validation boundaries.
