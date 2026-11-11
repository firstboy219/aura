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

## Addendum 2026-11-10 (P10-M5 review): detection and closing decide on the same evidence

Review of the live ledger found 4,165 entries in 16 hours. Detection used the whole lookback window while closing by evidence
used the most recent `MinClassRuns` runs, so a recovered weakness was resolved and reopened every 30-second cycle (a new entry
and a new R0/R2 remedy record each time). Detection now skips a weakness that closing would resolve at once (`recovered`, shared
by both); a returning failure is new evidence and opens an entry again. Improvement housekeeping tasks interrupted by a Core
restart are failed on recovery (`interrupted_improvement`) with their grants revoked instead of staying RUNNING. The self brief
counts only owner (`main`) tasks. Tests: `TestRecoveredLimitationIsNotReopenedByOldFailures`,
`TestInterruptedImprovementTaskIsFailedOnRecovery`. The duplicate entries already stored are history; removing them is an owner
decision (dismiss), not something the loop does to its own record.

## Addendum 2026-11-11 (P10-M5b): re-test evidence must be about the remedy

Production record before this change: R2 remedies 3 promoted, 98 rolled back, 2,269 escalated; re-test tasks 98 completed, 309
failed, of which 282 with PROVIDER_UNAVAILABLE. Three defects made the loop ineffective:

1. **Wrong baseline.** A repeated-failure-code limitation stored its failure *count* (for example 3) as the baseline, and the
   evaluator compared the exposing tasks' success *rate* with it, so such a remedy could never be promoted (13 remedies with
   every re-test passing were rolled back). The baseline is now the class success rate for a success-rate limitation and zero for
   a failure-code limitation (its exposing tasks all failed with that code).
2. **Infrastructure failures counted as regressions.** A fixture that ends with PROVIDER_UNAVAILABLE, WORKER_CRASH or KILL_SWITCH
   is inconclusive: it is run again after a pause (10 min, at most 3 attempts, only when improvement work is allowed) and never
   counts for or against the remedy. APPROVAL_EXPIRED, INPUT_TIMEOUT and cancellation are inconclusive without a re-run. A re-test
   waiting for the owner is ended by the Core: improvement work never asks the owner (AGENTS rule 18). With no conclusive run of
   an exposing task the remedy is withdrawn with the reason "inconclusive" (24 h cooldown as for any rollback) and no R4 follows.
3. **Transient draft failures were escalated to the owner.** 2,204 of the escalations were "route circuit is open" while
   drafting. A draft that could not run stays proposed and is tried again after 30 min, at most 4 attempts; only a draft that ran
   and was refused (schema, protected topic, no exemplars), or the last attempt, is escalated.

Nothing here widens a permission or touches an L4 component; promotion still needs a VERIFIED run, improvement beyond the noise
margin and no regression. Tests: `TestRetestInfrastructureFailureIsRerunNotHeldAgainstTheRemedy`,
`TestRetestWithoutEvidenceIsRecordedAsInconclusive`, `TestWaitingRetestIsReleasedAndLeftOut`,
`TestRemedyDraftTransientFailureWaitsInsteadOfEscalating`.
