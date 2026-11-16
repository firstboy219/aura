# ADR-0072 — ODI A3: approval accountability and revocation, tamper-evident audit, risk labels

Status: **Proposed** — awaiting owner approval · Date: 2026-11-16 · Evidence: unit and engine tests (implementation-verified); not yet production-proven

## Context
Blueprint v1.1 §8–§9 and §20. A0 found: approvals already bind to a canonical action hash, are single-use and expire (`policy.TestApprovalIntegrity`), and grants are short-lived, stored as hashes and revoked per task; but the audit log is append-only only through SQLite triggers (a privileged actor can drop them), approvals record nothing about how the approver authenticated, there is no revoke, and risk classes R0–R4 do not exist.

## Decision (migration 0019, additive: nullable columns only, no table rebuilt)
1. **Audit hash chain.** `audit_events` gains `prev_hash` and `hash` = sha256 over length-prefixed (prev, id, type, actor, task_id, request_id, data_json, at). Rows written before the migration stay unchained ("legacy"). `policy.VerifyAuditChain` re-computes it and finds an edited row, a row removed or inserted inside the chain, and an unchained row after the chain started. `GET /api/v1/admin/audit/verify` returns 200 or 409. The head hash must be anchored outside the database to catch tail deletion — **an owner decision** (see below). The triggers stay.
2. **Approvals** record `nonce`, `auth_strength` (derived from how the approver authenticated: web step-up TOTP, web session, owner token, member token), `risk_class` and `policy_version`, and `consumed_at`/`revoked_at`/`revoke_reason`. The optional nonce is not yet *required* from clients (Jarvis would need to send it back; additive first).
3. **Revoke.** `POST /api/v1/approvals/{id}/revoke` withdraws a pending or approved-but-unused approval: status `invalidated` (the CHECK is untouched) with `revoked_at` set, audit `APPROVAL_REVOKED`, the step and task fail with reason `approval_revoked` (OWNER_REJECTED) and the task's grants are revoked. A consumed approval cannot be revoked.
4. **Risk labels** (`policy.RiskOf`, version `risk-policy-v1-proposed`): A3 → R4 (hard-deny, unchanged); write_external at A2 → R3, at A0 → R2; write_idempotent at A2 → R2, at A0 → R1; read/pure at A2 → R1, at A0 → R0. **Labels only: nothing in A0–A3 enforcement is relaxed.** The mapping is a proposal; the owner approves or replaces it.
5. **Acceptance:** AT-07 (injection cannot widen authority) is recorded as met from existing tests (`TestDynamicFetchWithoutProvenanceDenied`, `TestOutOfScopeRequestsDeniedAndAudited`, `TestA3ForbiddenWrite`, `TestDelegationGrantWideningDenied`).

## Owner decisions still open
- Where to anchor the audit head (e.g. a periodic copy into Jarvis's database, an e-mail, an external log); who may export the audit.
- The R0–R4 mapping; whether any R3 class may ever be delegated.
- Whether approval clients must send the nonce (breaking for old clients).

## Rollback
Revert the commit. The new columns stay in the database (nullable, ignored by the old code); no data is lost. Rows written under the chain simply have extra column values.
