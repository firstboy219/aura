# ADR-0030 — Limitation Ledger v1 and the closed self-improvement loop (Blueprint §8A.5)

Status: **Proposed** (checkpoint v1.3.11, 2026-11-09).

## Decision
- **Records**: `limitations` (versioned; status open → diagnosing → remedy_proposed → remedy_testing → resolved | rolled_back |
  escalated | wont_fix; category; task_class; evidence as references and counts only; impact; diagnosis; baseline/result) and
  `limitation_remedies` (class R0–R6, status, artifact, before/after metrics). Workspace-scoped (one DB per workspace).
- **Detection is statistical** (no model): verified success rate of a task class below target with ≥ 10 runs; the same canonical
  reason code ≥ 3 times; safety events (`CAPABILITY_DENIED`, `PROTECTED_PROPOSAL_REJECTED`) open at once and are escalated; owner
  feedback. One failure never opens one; model self-report never opens or closes one. **Task class (v1)** = sorted tool set
  (PROPOSAL-0002 asks for a richer definition).
- **Diagnosis**: deterministic mapping from reason codes (`taxonomy.CandidateCause`) with a confidence; only when confidence < 0.6
  and improvement budget remains, the `standard` tier proposes a cause from the **fixed taxonomy** (plan/prompt strategy, missing
  or weak skill, missing tool, wrong tier/route, knowledge/data gap, external dependency, defect in AURA code, permission
  boundary); an answer outside the taxonomy is discarded.
- **Remedy routing** (`taxonomy.RemedyClass`): external dependency → R0 (recorded, already self-healing); wrong tier/route → R1
  (proposal to the owner; automation is P3b); plan strategy / weak skill → **R2 (acts automatically)**; missing tool → R3
  (escalated until P4); code defect → R5 (proposal only); permission boundary and knowledge gap → **R6 owner request — never a
  permission widening**.
- **R2 loop**: draft a corrective skill from failed and successful exemplars (`task.remedy_skill`, standard tier) → save as a new
  skill version → re-test by re-running the exposing tasks plus a regression sample as low-priority `retest` tasks → promote only
  if the success rate on the exposing tasks beats the baseline by more than the noise margin (default 10 points) with no
  regression; otherwise roll back the skill (previous version, or disable) and record the reason. Resolved entries keep being
  monitored by detection: a regression opens a new entry.
- **Improvement budget**: tasks with `agent_role` improvement/retest are accounted separately (default cap USD 0.25/day in the
  workspace time zone), priority below every owner task, run only when no owner task is runnable, and stop with the kill switch.
- **Honest disclosure**: `/self` and `/self-model` print open limitations with measured rates, computed without a model.
- **L4 protection (AGENTS rule 23)**: no runtime path lets the loop change the Core, policies, the Charter or the evaluation
  harness; skills can only use manifest tools, and policy/identity/Charter topics are rejected and audited.
