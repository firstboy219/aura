# ADR-0025 — Learning L1: lessons and skills (P3)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- After a task ends SUCCEEDED **and** VERIFIED, the Core calls `task.reflect` (fast tier). The worker may propose one
  lesson (memory kind `lesson`) and one skill: name, trigger terms, a plan template (tool steps with placeholders),
  and notes. Skills derived from unverified tasks are not created.
- Skills are versioned rows (`skills` + `skill_versions`); a new proposal with the same name creates a new version
  (candidate → active only if its source task was verified). Owner controls: disable/enable, rollback to a version,
  diff between versions. Skills never carry permissions: a skill's tools are still subject to the task's grant.
- Planning receives up to three matching active skills (FTS on name + triggers) with their source task ids.
