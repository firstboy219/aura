-- ODI (owner decision 2026-11-17, option A): a heavy task that NEXUS cannot take (its context is too small for it, or it cannot finish in time) may be served by an
-- external AI, free routes first, public data only, and NEXUS learns from the answer. That is a third, evidence-based trigger of a fallback authorization.
-- SQLite cannot change a CHECK in place: the (empty or small) table is rebuilt with the wider CHECK. No other table references it.
CREATE TABLE fallback_authorizations_new (
  id TEXT PRIMARY KEY,
  task_id TEXT,
  purpose TEXT NOT NULL,
  trigger_code TEXT NOT NULL CHECK (trigger_code IN ('NEXUS_UNAVAILABLE','OWNER_DISABLED','NEXUS_CAPACITY_EXCEEDED')),
  evidence_ref TEXT NOT NULL,
  providers_json TEXT NOT NULL,
  data_classes_json TEXT NOT NULL,
  budget_cap_usd REAL NOT NULL,
  max_calls INTEGER NOT NULL,
  used_calls INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('active','revoked','expired','exhausted')),
  revoke_reason TEXT,
  audit_ref TEXT,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER);
INSERT INTO fallback_authorizations_new SELECT * FROM fallback_authorizations;
DROP TABLE fallback_authorizations;
ALTER TABLE fallback_authorizations_new RENAME TO fallback_authorizations;
CREATE INDEX fallback_active ON fallback_authorizations(state, expires_at);
