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

## Agents
- Hermes: scoped API/runtime implementation.
- OpenClaw: isolated domain implementation.
- Antigravity: infrastructure/integration implementation.
- Cursor: precise repository edits, minimal diffs, local refactors and fast test feedback.
- Anorak/Reviewer: validates specs, actual diff, tests, regression and evidence.

## Human gate
Required for product/architecture changes, destructive migrations, security/auth, secrets, production, material cost or unspecified decisions.

## Review
PASS -> Done -> unblock dependents.
FAIL -> In Progress -> same owner -> targeted fix -> Review.
