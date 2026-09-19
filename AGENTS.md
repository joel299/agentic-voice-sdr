# Agent Operating Contract

Every engineering agent must load, in order:
1. assigned Linear issue;
2. Prompt Cache;
3. PRD;
4. SDD;
5. relevant ADRs;
6. TDD;
7. Loop Engineering;
8. Shared Memory context.

## Mandatory loop

SPEC -> TEST -> RED -> IMPLEMENT -> GREEN -> REFACTOR -> REGRESSION -> EVIDENCE -> REVIEW.

## Scope
- Implement only the assigned microtask.
- Do not refactor unrelated code.
- Do not invent features.
- Do not change architecture for convenience.
- Do not silently add structural dependencies.

## Official execution roster
- Stark / Hermes: backend/core implementation — Go runtime, API, HTTP/WebSocket, telephony/AudioSocket boundaries and internal services.
- Neriel / OpenClaw: domain/business logic — state machines, domain rules, agent behavior and tool orchestration.
- Arquimedes / Antigravity: infrastructure/integrations — Redis, RabbitMQ, Docker/deploy, operational contracts and external integrations.
- Anorak / Reviewer: validates specs, actual diff, tests, regression, evidence and scope. Reviewer does not execute implementation microtasks.

Cursor is retired from task execution and must not be assigned as owner of new microtasks.

## Human gate
Required for product/architecture changes, destructive migrations, security/auth, secrets, production, material cost or unspecified decisions.

## Review
PASS -> Done -> unblock dependents.
FAIL -> In Progress -> same owner -> targeted fix -> Review.

## Dual tracking

Every GRU must be tracked in both Linear and GitHub.

Mandatory:
- create the Linear GRU;
- create a GitHub Issue mirror for the same GRU;
- link Linear -> GitHub Issue and GitHub Issue -> Linear;
- when implementation changes repository files, link the PR from both task records;
- record review outcomes and correction requests in Linear, the GitHub Issue, and the PR;
- keep GitHub Issue open while Linear is Backlog, Todo, In Progress, or In Review;
- close the GitHub Issue only after Anorak moves Linear to Done.

Linear is the control plane for state/dependencies. GitHub is the repository-side durable task/evidence mirror.

## CI gate

Every implementation PR must pass the repository GitHub Actions workflow before returning to Review.

Required automated checks:
- Scope Policy
- Diff Quality
- Go Quality when Go sources are present
- Redis RabbitMQ Infra when dev infrastructure is present

Rules:
- A red CI check means the issue stays or returns to In Progress.
- Executor-written claims are supporting context, not a substitute for CI.
- The branch must remain inside the Linear issue ALLOWED_SCOPE.
- Fix the same branch; do not open a replacement PR unless explicitly instructed.
- After corrections, push the branch, wait for automated checks, then mark the PR Ready for Review and move Linear to In Review.


## Arquimedes execution mandate

When a GRU is assigned to Arquimedes / Antigravity, Arquimedes MUST execute the task end-to-end.

Analysis, diagnosis, planning, recommendations, command lists, or instructions-only output do not satisfy the task.

Arquimedes must perform the actual changes within ALLOWED_SCOPE, run validation/tests, push commits, create/update the PR, fix CI until green, update Shared Memory, and synchronize Linear + GitHub Issue + PR.

Arquimedes may stop before execution only for an explicit HUMAN_GATE, a real external blocker, missing indispensable permission/tooling, or an out-of-scope conflict requiring human decision.

If none of those conditions exists, analysis without execution is incomplete work.
