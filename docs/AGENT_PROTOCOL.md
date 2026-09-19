# Agent Operating Protocol — Agentic Voice SDR

This protocol governs all autonomous AI agent interactions, development workflows, quality gates, and handoffs within the `agentic-voice-sdr` repository.

## Agent Execution Roster

| Agent Name | Engine / Persona | Role & Domain Responsibilities |
| :--- | :--- | :--- |
| **Stark** | Hermes | **Backend & Core Infrastructure**: Go runtime, API layer, HTTP/WebSocket endpoints, AudioSocket boundaries, and low-level internal services. |
| **Neriel** | OpenClaw | **Domain & Business Logic**: State machines, call session transitions, business policies, and tool orchestration. |
| **Arquimedes** | Antigravity | **Dev Infrastructure & Integrations**: Redis, RabbitMQ, PostgreSQL containers, Docker Compose, deployment scripts, and operational topology. |
| **Anorak** | Reviewer | **Quality Assurance & Verification**: Code review, spec alignment, TDD verification, regression analysis, and final issue approval. *Does not execute feature code.* |

> [!CAUTION]
> **Retired Agents**: Cursor is retired from task execution and MUST NOT be assigned as owner of new microtasks or PRs.

## Agent Operating Workflow

Every agent must follow this execution loop for every microtask:

```text
[Backlog Issue]
      │
      ▼
 1. Load Specs (PRD, SDD, ADR, TDD, AGENTS.md, Shared Memory)
      │
      ▼
 2. Write Failing Test (RED) -> Run `go test -race ./...`
      │
      ▼
 3. Implement Minimum Code (GREEN) -> Verify test passes
      │
      ▼
 4. Refactor & Clean -> Verify zero regression
      │
      ▼
 5. Run Local Verification & Health Checks
      │
      ▼
 6. Push to Dedicated Branch -> Wait for GitHub Actions CI (100% Green)
      │
      ▼
 7. Register Shared Memory Entry -> Move Linear to `In Review`
      │
      ▼
 8. Handoff to Anorak (Reviewer) for Final Review
```

## Mandatory Sign-Off Protocol
All updates to Linear, GitHub PR descriptions, Shared Memory entries, and handoff reports **MUST** be signed off with the assigned agent name:

Example: `— Arquimedes`

## Human Gate Rules
A **HUMAN_GATE** requires explicit human review and approval before proceeding when encountering:
1. Fundamental product or architectural changes not covered by existing ADRs.
2. Destructive operations (dropping database tables, deleting remote repositories).
3. Modifying repository visibility (e.g. `PUBLIC` vs `PRIVATE`).
4. Secret management, authentication schemes, or production environment alterations.
5. Unbudgeted material cloud infrastructure costs.

## CI & Review Lifecycle Rules
- **State Machine**: `Backlog` -> `Todo` -> `In Progress` -> `In Review` -> `Done`.
- **On Rejection**: If Anorak requests changes, issue transitions back to `In Progress`. The assigned owner performs targeted fixes on the **same PR and branch**, re-verifies CI, and resubmits to `In Review`.
- **Zero Parallel Overlap**: Agents must never edit overlapping core files without explicit authorization.
