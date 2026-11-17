# ADR-0071 — ODI A2: derived canonical state, verification levels and truthful claims

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Context
Blueprint v1.1 §5 and §15 want every task to report a canonical state, state the real evidence level (V0–V3) behind a result, and never call a task done because a tool call was merely issued (AT-09). AURA persists 12 states and already verifies against completion criteria, but nothing said at what level a result was checked.

## Decision (additive; nothing stored, no schema change, no state added)
1. `internal/odi.Derive` computes, from the persisted task, its steps and a few event types: the canonical state (`PLANNED`, `VERIFYING`, `PARTIAL`, `EXPIRED`, `RECOVERY_REQUIRED` are *derived*; `WAITING_INPUT`, `WAITING_EXTERNAL`, `SCHEDULED` stay documented extensions), the verification level, step counts and a **claim**.
2. Levels: V0 attempted (`TOOL_INTENT/STARTED`), V1 the tool reported success (`TOOL_SUCCEEDED`), V2 a postcondition was observed (`VERIFIED`, the existing completion-criteria check), V3 authoritative confirmation (event `AUTHORITATIVE_CONFIRMED`; nothing emits it yet). A step with no tool has no level.
3. A new internal event `VERIFY_STARTED` (hidden from the timeline) marks the verification phase, which makes `VERIFYING` derivable.
4. `GET /api/v1/tasks/{id}` gains an optional `assurance` object (OpenAPI `Assurance`). The list endpoint is unchanged.
5. The owner-facing reply gets a note `unverified_outcome` when a write step has no V2/V3 evidence, so "done" is never stronger than the evidence.
6. Claims: `completed_verified` only at V2/V3; a V1 result is `completed_unverified`; V0 is `attempted_only`; an unknown external outcome is `uncertain` (and canonical `RECOVERY_REQUIRED`).

## Consequences
Clients may ignore the new fields; Jarvis can show the claim and the note. `BUDGET_EXCEEDED` maps to `FAILED`, except `max_wall_seconds`, which is `EXPIRED` (owner decision still pending on the mapping). `READY` and `RECEIVED/VALIDATING/REJECTED` remain unpersisted (they occur in the API before a row exists). Rollback: revert the commit; nothing was migrated.
