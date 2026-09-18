# Loop Engineering v1.0

Lifecycle:
BACKLOG -> TODO -> IN PROGRESS -> IN REVIEW -> DONE.

On rejection:
IN REVIEW -> IN PROGRESS -> FIX -> TEST -> IN REVIEW.

Agents do not busy-poll Linear; event/webhook orchestration should reactivate them.

Parallel execution is allowed only when critical files/contracts do not overlap.

Escalate after three repeated review failures for the same root cause or when HUMAN_GATE is required.
