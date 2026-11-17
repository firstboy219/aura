# ADR-0019 — Subagents v1 (sequential) and Shared Task Context

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- An `agent.delegate` step creates a child task: `parent_task_id`, `root_task_id`, `agent_role: specialist`,
  `depth+1` (max depth 2), a briefing (objective, constraints, relevant context ids), tools ⊆ parent tools,
  domains ⊆ parent domains, budget carve-out ≤ the parent's remaining budget (reserved from the parent).
- Children run sequentially through the normal scheduler; the parent waits on them (`task_waits` kind `tasks`).
- Shared Task Context (`shared_context`) is per root task: entries with kind (finding, progress, question,
  plan_change, owner_correction), author task, trust (`owner` | `core_verified` | `untrusted`) and provenance.
  Children read it in every step's observations; only the main task talks to the owner.
- A child grant request outside its subset or budget is denied and audited. Cancelling the root cancels all
  descendants. Traces show one tree with rolled-up cost (`GET /tasks/{id}/tree`).
