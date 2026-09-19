# Engineering Guidelines — Agentic Voice SDR

## Core Principles

1. **Contract-first and spec-driven development**
   - Every feature must be backed by the approved PRD, SDD, ADRs, and task contract.
   - Resolve ambiguities before writing functional code.

2. **Test-driven development**
   - Follow the mandatory loop:
     `SPEC -> TEST (RED) -> IMPLEMENT (GREEN) -> REFACTOR -> REGRESSION -> EVIDENCE -> REVIEW`.
   - Run `go test -race ./...` where concurrency is relevant.

3. **No fallback credentials in runtime**
   - Environment variables and secrets must be explicitly passed.
   - Development stacks fail fast when required environment variables are absent.

4. **Zero audio in event pipelines**
   - Raw audio frames remain isolated on the approved realtime path and do not enter Redis, RabbitMQ, PostgreSQL, n8n, or unrelated external integrations.

## Repository Layout

```text
.
├── .github/                 # GitHub Actions and repository contribution policy
├── cmd/                     # Application entrypoints
├── deploy/                  # Development infrastructure configuration
├── docs/                    # Product, system, ADR, and engineering documentation
├── internal/                # Private application packages
├── scripts/                 # Verification and development utilities
├── AGENTS.md                # Agent operating contract
├── CHANGELOG.md             # Release changelog
├── README.md                # Repository landing overview
└── VERSION                  # Current semantic version marker
```

## Technology Baseline

The authoritative technology versions and approved components are maintained in [`docs/TECHNOLOGY_BASELINE.md`](TECHNOLOGY_BASELINE.md). This document does not infer or introduce versions that are not explicitly fixed there.

The currently confirmed versioned entries are:

- **Go**: `1.27.x`
- **Chi**: `v5`

Other approved technologies are referenced without an inferred version: REST/OpenAPI, WebSocket events, Asterisk/AudioSocket, Gemini Live, PostgreSQL/Supabase, Redis, RabbitMQ, Transactional Outbox, Tool Registry, Composio, WhatsApp API, Docker, and OpenTelemetry.

## CI/CD and Quality Gates

Every pull request must pass the repository GitHub Actions workflow before review:

- **Scope Policy**: changes match the assigned Linear issue scope.
- **Diff Quality**: formatting and diff hygiene are clean.
- **Go Quality**: compilation, tests, and static checks pass when Go sources are present.
- **Infrastructure validation**: applicable development infrastructure checks pass when infrastructure files are present.
