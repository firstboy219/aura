# ADR-0029 — Task state names and the representation of INTERRUPTED / RECOVERING

Status: **Proposed** (checkpoint v1.3.11, 2026-11-09).

## Context
Blueprint §8.2 names the states `CREATED, PLANNING, RUNNING, WAITING_APPROVAL, WAITING_EXTERNAL, WAITING_INPUT, SCHEDULED,
PAUSED`, terminal `COMPLETED, FAILED, CANCELLED`, and requires the P0 ADR to choose one consistent representation for
`INTERRUPTED`/`RECOVERING`. P1/P2 used `SUCCEEDED` and `WAITING`.

## Decision
- Renamed to the Blueprint names (migration 0003 rebuilds `tasks` and maps `SUCCEEDED→COMPLETED`, `WAITING→WAITING_EXTERNAL`).
  `SCHEDULED` is used for time waits (`wait.until`, future start); `WAITING_EXTERNAL` for events and child tasks.
- **`INTERRUPTED` and `RECOVERING` are explicitly modeled events with deterministic projections**, not persisted states:
  on Core start, tasks in `PLANNING`/`RUNNING` get `TASK_RECOVERED` (and `STEP_RECOVERED` per started step, with the action
  `rerun | replay_recorded_intent | outcome_unknown`); the lease (`lease_owner`, `lease_expires_at`) shows who drives a task.
  Reason: a persisted state would make a crash leave tasks in a state only the next Core can leave; events keep the
  projection trivially derivable and the state machine small.
- `BUDGET_EXCEEDED` stays a terminal outcome (Blueprint §8.4 "End with BUDGET_EXCEEDED"), and its canonical reason code is
  `BUDGET_EXCEEDED` too (§8.2 lists it as a reason code). Recorded as an assumption; the owner may fold it into `FAILED`.
- Every terminal/failed state records a canonical reason code (`internal/taxonomy`); steps do too.
- Recovery policy `auto | manual | never`: not implemented; recovery is always automatic except that an unknown external-write
  outcome is **never** retried automatically (it asks the owner, OUTCOME_UNKNOWN). Gap listed in the conformance report.
