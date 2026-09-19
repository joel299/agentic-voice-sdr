# Engineering Guidelines — Agentic Voice SDR

## Core Principles

1. **Contract-First & Spec-Driven Development**:
   - Every feature must be backed by a clear spec (`PRD`, `SDD`, `ADR`) prior to implementation.
   - Ambiguities must be resolved before functional code is written.

2. **Test-Driven Development (TDD)**:
   - Mandatory RED-GREEN-REFACTOR cycle:
     `SPEC -> TEST (RED) -> IMPLEMENT (GREEN) -> REFACTOR -> REGRESSION -> EVIDENCE -> REVIEW`.
   - Never write production logic without a failing test first.
   - Run race detection on all concurrent Go packages: `go test -race ./...`.

3. **No Fallback Credentials in Runtime**:
   - Environment variables and secrets must be explicitly passed.
   - Hardcoded default passwords (e.g. `devpassword`, `guest`) are strictly forbidden.
   - Development stacks must fail fast with clear diagnostic messages if required environment variables are absent.

4. **Zero Audio in Event Pipelines**:
   - High-throughput audio payload isolation is enforced. Audio frames exist only within the Go AudioSocket handler and Gemini Live WebSocket connection.

## Repository Layout
```text
.
├── .github/
│   ├── workflows/          # GitHub Actions CI/CD pipelines
│   └── pull_request_template.md
├── cmd/                    # Application entrypoints
├── deploy/                 # Docker Compose, K8s, and dev infrastructure config
│   └── dev/
│       ├── rabbitmq/       # RabbitMQ config & definitions
│       └── docker-compose.yml
├── docs/                   # System documentation, PRD, SDD, ADRs, Engineering specs
├── internal/               # Core private application packages
│   └── telephony/          # AudioSocket codec, TCP server, and SIP interfaces
├── scripts/                # Verification, health check, and dev utility scripts
├── AGENTS.md               # Agent Operating Contract & Roster
├── CHANGELOG.md            # Release changelog
├── README.md               # Repository landing overview
└── VERSION                 # Current semantic version tag
```

## Technology Stack Baseline
- **Language & Runtime**: Go 1.27.x
- **Cache & Key-Value**: Redis 7.x
- **Message Broker**: RabbitMQ 3.12+ (AMQP 0-9-1)
- **Database**: PostgreSQL 16 / Supabase
- **Telephony**: Asterisk 20+ with AudioSocket plugin
- **AI Engine**: Gemini Live API (Multimodal WebSocket API)

## CI/CD & Quality Gates
Every Pull Request must pass the repository GitHub Actions workflow before review:
- **Scope Policy**: Branch and PR changes must strictly match the assigned Linear issue scope.
- **Code Quality & Lints**: Clean compilation, zero unhandled errors, idiomatic Go formatting (`gofmt`, `golangci-lint`).
- **Test Suite**: 100% passing automated unit, contract, and integration tests (`go test -v -race ./...`).
- **Infrastructure Validation**: Dev stack initialization, configuration schema validation, and health checks.
