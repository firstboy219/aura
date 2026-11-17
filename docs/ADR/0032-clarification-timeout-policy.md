# ADR-0032 — Clarification timeout policy (`INPUT_TIMEOUT`)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
policy (remind, continue with stated assumption where allowed, or fail with `INPUT_TIMEOUT`)."

## Decision
- Config `inputs`: `remind_after_seconds` (default 4 h, 0 = no reminder), `timeout_seconds` (default 48 h),
  `on_timeout: fail | continue` (default **fail**). Validated at load.
- The Core's periodic tick checks pending questions. After `remind_after_seconds` it sends **one** owner notification (kind `info`)
  and an `INPUT_REMINDED` event. The task state does not change.
- At `timeout_seconds` the question becomes `timed_out` (new status; migration 0004 rebuilds `task_inputs` and adds `reminded_at`)
  with an `INPUT_TIMEOUT` event:
  - `fail`: the task goes to `FAILED`, reason `input_timeout` → canonical code `INPUT_TIMEOUT`, and its grants are revoked.
  - `continue`: the task resumes planning (or running). The planner is told that the owner did not answer, and that it should
    continue only if the objective can be met without the information, stating every assumption in the output. It may not ask
    again in that task.
- A clarification never grants permission (§8.2). Continuing therefore cannot widen scope. Every A2 action still needs its own
  approval.
- A question that times out while the task is paused simply lapses. A late answer is refused, because nothing is pending.

## Consequences
The default is the conservative choice: an unanswered task fails visibly instead of guessing. `continue` is an owner opt-in.
Tests: `engine/inputs_test.go` (reminder once, timeout → `INPUT_TIMEOUT`, continue path, no re-ask, config validation).
