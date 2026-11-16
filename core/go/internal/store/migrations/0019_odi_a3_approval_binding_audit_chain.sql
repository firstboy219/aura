-- ODI A3 (ADR-0072). Additive only: every column is nullable, no row is rewritten, no table is rebuilt.
-- Approvals: a nonce, how the approver authenticated, the risk class and the policy version the decision was made under, and when the approval was consumed
-- or revoked. (A revoked approval has status 'invalidated' and revoked_at set; the status CHECK is not changed.)
ALTER TABLE approvals ADD COLUMN nonce TEXT;
ALTER TABLE approvals ADD COLUMN auth_strength TEXT;
ALTER TABLE approvals ADD COLUMN risk_class TEXT;
ALTER TABLE approvals ADD COLUMN policy_version TEXT;
ALTER TABLE approvals ADD COLUMN consumed_at INTEGER;
ALTER TABLE approvals ADD COLUMN revoked_at INTEGER;
ALTER TABLE approvals ADD COLUMN revoke_reason TEXT;
-- Audit: a hash chain (Blueprint §20). Rows written before this migration keep NULL hashes ("legacy"); every row after it carries
-- hash = sha256(prev_hash | id | type | actor | task_id | request_id | data_json | at). The append-only triggers stay.
ALTER TABLE audit_events ADD COLUMN prev_hash TEXT;
ALTER TABLE audit_events ADD COLUMN hash TEXT;
