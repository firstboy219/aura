# ADR-0016 — P2 agent loop: replanning, verification, plan review, clarification, cascade

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Replanning** is bounded: at most 2 replans per task (step failure or failed verification). The worker's
  `task.replan` returns replacement steps for the remaining work; pending steps are marked `SKIPPED`, new steps get
  the next step numbers. Every replan is an event with its cause.
- **Verification** after the last step: deterministic checks first (expected outputs exist, recorded hashes match),
  then `task.verify` (fast tier; escalated to standard when the fast answer is invalid or low-confidence) against the
  plan's completion criteria with the produced file contents as evidence. Model self-confidence is never evidence by
  itself: a pass needs every criterion marked met with a quoted excerpt that the Core finds in the output.
- **Plan review** (fast tier) runs only when the plan has ≥ 6 steps, delegates to subagents, or contains an A2 step.
- **Clarification**: the planner may return `question` instead of steps when the objective is ambiguous; the task
  enters `WAITING_INPUT`; the owner's answer is added to the objective context (trust: owner) and planning repeats.
- **Cascade**: fast for review/verification, standard for planning/writing/replanning, deep only after a second
  verification failure or when the owner sets `tier: deep` on the task. The policy lives in the worker; the Core
  records the tier of every call.
