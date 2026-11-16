# ADR-0077 — ODI A8: self-engineering change levels, approval rules and evidence-gated rollout

Status: **Proposed** — awaiting owner approval · Date: 2026-11-16 · Evidence: unit tests (implementation-verified)

## Context
Blueprint v1.1 §16. AURA already has sandboxed L2 tools (approved by the sha256 of their exact source), the Limitation Ledger and Lab, and a runtime guard that rejects proposals touching policy, permissions, identity or the Charter (`PROTECTED_PROPOSAL_REJECTED`). What it lacked was one place where *any* change proposal is classified by level, checked for weakening its own evidence, approved by the right person, and moved to "accepted" only with evidence of the deployment.

## Decision (migration 0023: one table; nothing here changes code by itself)
1. **Classification by path** (`odi.Classify`): the highest level of any path touched. L0 documentation; L1 skills and benchmark data; L2 source, contracts, workflows; **L3 = the protected safety invariants**: policy/approvals/audit chain, vault, egress, NEXUS gateway/fallback/Teacher Gate, device identity, sandbox and worker host, owner authentication, budget/kill switch, gateway, migrations, `deploy/`, `shell/`, the acceptance catalogue and the owner documents. An unknown path is L2. A test fails if a protected path no longer exists.
2. **Proposal record** (`odi.Proposal`): symptom, evidence, root-cause confidence (low|medium|high; no invented numbers), components, risks, test plan, rollback plan, expected outcome. From L2, tests, rollback and risks are mandatory.
3. **Weakening guard** (`odi.WeakensTests`, heuristic): a deleted test file, a newly skipped test, a test file losing more assertions than it gains, or an acceptance status set to `met` without its test. A finding **blocks** the change (it cannot be approved).
4. **Approval rules**: L0 and owner-pre-approved L1 are approved automatically after the checks; other L1 and every L2 need the owner; **L3 needs the owner with a fresh second factor** (web TOTP); a change proposed by an agent (`agent:<name>`) needs that second factor from L2, because an automated proposer may hold the owner's API token; nobody but the owner approves their own proposal; a model or worker never approves. Rollout moves (`in_canary → deployed → accepted | rolled_back`) are legal only in order; **acceptance needs `post_deploy_health` evidence with `ok=true` and the owner; a rollback needs a reason**. Every step is audited.
5. Endpoints: `POST/GET /admin/changes`, `GET /admin/changes/{id}`, `POST .../decide`, `POST .../move`.

## Not done
No automatic deployment, canary traffic splitting or rollback execution: AURA records and gates the change, the existing release tooling (`deploy/release.sh`) deploys it. Verified learning stays with the memory pipeline (a lesson is active only if its evidence quote is found). The weakening guard is a heuristic and can be bypassed by a determined author; the second factor and a human reading the diff remain the real controls.

## Rollback
Revert the commit; the table stays unused.
