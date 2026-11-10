# ADR-0048 — L3 brain variants through AURA Lab (R4 remedies)

Status: **Proposed** (P6, 2026-11-10). Implements these items of D.1 P6:
- "R4 brain remedies flow from ledger entries through AURA Lab with owner approval and auto-rollback";
- "L3 improvement beats baseline without safety regression; owner approval; staged rollout and automatic rollback; L4
  remains proposal-only";
- "AURA Lab resource isolation and cleanup proven".

## Decision
- **What a variant is.** A variant is plain-prose *guidance* for one reasoning module: `planner`, `replanner` or `verifier`.
  - It is at most 1500 characters, with no code or markup (Core gate `checkGuidance`).
  - The worker adds it to that module's instructions, labelled as owner-approved guidance that never changes tools,
    permissions or rules.
  - A variant is not code. Changes to the Python reasoning code itself keep shipping as signed releases (ADR-0037), because
    the base release is never modified in place (Blueprint §16.9 overlay model).
  - The Core enforces grants, tiers, approvals and budgets the same way whatever the guidance says. L4 stays unreachable.
- **Entry.** An R4 remedy starts only from a Limitation Ledger entry, under all of these conditions:
  - the diagnosed cause is `plan_strategy`;
  - the R2 skill remedy for that entry was rolled back;
  - `lab.enabled` is true.
  An entry gets one R4 attempt.
- **AURA Lab evaluation.**
  - The worker drafts the variant from the entry and its tasks.
  - The Lab replays up to 3 exposing (failed) and 2 regression (successful) tasks of the class, each twice: once with the
    current version (baseline arm) and once with the variant.
  - The variant passes only if all of these hold:
    - it beats the baseline overall;
    - it is not worse on the exposing tasks or the regression tasks;
    - it has no more capability denials than the baseline (no safety regression).
- **Lab isolation.**
  - Lab tasks run as `agent_role=lab` at low priority, with budgets capped by `lab.max_task_cost_usd`. Their spend counts
    against the improvement daily budget.
  - Only exemplars limited to read/compute tools and workspace writes are replayed.
  - A Lab task can never ask the owner for an approval (`lab_no_approval` denial).
  - Lab work keeps the data class of the original task.
  - After the verdict, the Lab tasks' workspaces are deleted (`lab_runs.cleaned_at`). The records stay for the audit trail.
- **Owner approval.**
  - A variant that passed waits for the owner: `aura brain approve`, or the API with TOTP step-up on the web.
  - The approval is bound to the variant's sha256, audited, and owner-role only.
- **Staged rollout and automatic rollback.**
  - An approved variant first serves `lab.canary_percent` of new tasks (deterministic by task ID).
  - After `lab.canary_min_tasks` finished canary tasks, the canary is compared with the control group, or with the Lab
    baseline when there is no control group:
    - it is rolled back automatically if its verified success rate is lower by more than the noise margin, or if it has
      more capability denials per task;
    - otherwise it is promoted to active, replacing the module's previous active variant.
  - The owner can roll back at any time.
  - Every task that receives guidance records a `BRAIN_GUIDANCE` event naming the variants it received.
- **Privacy fix found while building this.** Improvement tasks were always `public`, so a remedy drafted from the owner's
  private tasks or notes could reach routes that may log prompts. An improvement task is now `private` whenever its inputs
  include a private task or an owner note. This applies to the R2, R3 and R4 drafting tasks and to diagnosis.

## Alternatives
- Let AURA edit its Python reasoning code at runtime: rejected. It breaks the signed-release model, and code-level change
  cannot be bounded the way module guidance can.
- Approve without a Lab comparison: rejected by D.1 P6 ("beats baseline without safety regression").

## Evidence (fake provider, `engine/lab_test.go`)
- `TestP6BrainRemedyThroughLab` covers the full path: ledger, R4 proposal (refused while the Lab is off, at most once),
  draft, 10 paired Lab runs, verdict, cleanup, no approvals asked, no guidance in service before approval, wrong-hash
  approval refused, canary, promotion, resolved entry, audit trail, owner rollback.
- `TestP6LabRejectsVariantThatDoesNotHelp`: a variant that does not help is `lab_failed`, cannot be approved, and the entry is
  rolled back.
- `TestP6CanaryAutoRollback`: a canary that does worse than the Lab baseline is rolled back by the Core.
- `TestP6GuidanceGate` and `TestImprovementTaskInheritsPrivacy`.

## Limits
- `lab.enabled` is false in production until the owner enables L3. Blueprint §16.9 lists which evolution levels are enabled
  as an owner setting.
- No real-model Lab run yet. Lab runs with private exemplars need a paid route, and the OpenRouter credit is 0.
- Small samples: 5 paired tasks and a canary minimum of 6 tasks are a weak statistical signal. The noise margin and the
  owner approval are the safeguards.
