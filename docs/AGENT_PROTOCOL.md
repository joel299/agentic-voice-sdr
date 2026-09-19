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

## Mandatory Context Loading Order

Every engineering agent must load, in order:
1. assigned Linear issue;
2. Prompt Cache;
3. PRD;
4. SDD;
5. relevant ADRs;
6. TDD;
7. Loop Engineering;
8. Shared Memory context.

## Agent Operating Workflow

Every agent must follow this execution loop for every microtask:

```text
[Backlog Issue]
      │
      ▼
 1. Load Context in Mandatory Order (Linear issue -> Prompt Cache -> PRD -> SDD -> ADRs -> TDD -> Loop Engineering -> Shared Memory)
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
 7. Perform Mandatory Dual Tracking & Synchronization:
    a. Create or confirm GitHub Issue mirror for assigned Linear GRU
    b. Establish bidirectional cross-links: Linear -> GitHub Issue and GitHub Issue -> Linear
    c. Link PR in both Linear issue and GitHub Issue
    d. Register Shared Memory handoff entry via MCP
    e. Register review/correction outcomes in Linear + GitHub Issue + PR
    f. Confirm GitHub Issue remains OPEN (closed ONLY after Anorak moves Linear to Done)
      │
      ▼
 8. Move Linear Issue to `In Review`
      │
      ▼
 9. Handoff to Anorak (Reviewer) for Final Review
```

## Dual Tracking & Issue Mirroring Rules

Every microtask (GRU) requires complete dual tracking between Linear and GitHub Issues:
- **Canonical Reference**: `AGENTS.md` is the canonical reference for repository governance and dual tracking rules.
- **Issue Mirroring**: Every Linear GRU must have a corresponding GitHub Issue mirror.
- **Bidirectional Cross-Linking**: The Linear GRU must link to the GitHub Issue URL, and the GitHub Issue description/comment must link back to the Linear GRU.
- **PR Association**: Any pull request containing repository changes must be linked in both the Linear GRU and the GitHub Issue mirror.
- **Review & Correction Outcomes**: All review outcomes, feedback, and correction requests must be logged across Linear, the GitHub Issue mirror, and the PR.
- **Issue Lifecycle**: The GitHub Issue mirror **MUST REMAIN OPEN** while the Linear GRU is in `Backlog`, `Todo`, `In Progress`, or `In Review`. The GitHub Issue is closed **ONLY AFTER** Anorak completes final verification and transitions the Linear GRU to `Done`.

## Mandatory Sign-Off Protocol

All updates to Linear, GitHub PR descriptions, Shared Memory entries, and handoff reports **MUST** be signed off with the assigned agent name:

Example: `— Arquimedes`

## Human Gate Rules

A **HUMAN_GATE** requires explicit human review and approval before proceeding.
HUMAN_GATE is required for **ANY** product or architecture change, regardless of whether an ADR exists, whether it is considered fundamental, or whether prior documentation exists.

Specific triggers requiring a **HUMAN_GATE** include:
1. Product or architecture changes (any modification to product features, component boundaries, domain contracts, or system architecture).
2. Destructive operations (dropping database tables, deleting remote repositories or branches).
3. Modifying repository settings or visibility (e.g. `PUBLIC` vs `PRIVATE`).
4. Secret management, authentication schemes, or production environment alterations.
5. Unbudgeted material cloud infrastructure costs.
6. Unspecified design or architectural decisions where requirements are ambiguous.

## CI & Review Lifecycle Rules

- **State Machine**: `Backlog` -> `Todo` -> `In Progress` -> `In Review` -> `Done`.
- **On Rejection**: If Anorak requests changes, issue transitions back to `In Progress`. The assigned owner performs targeted fixes on the **same PR and branch**, re-verifies CI, and resubmits to `In Review`.
- **Zero Parallel Overlap**: Agents must never edit overlapping core files without explicit authorization.
