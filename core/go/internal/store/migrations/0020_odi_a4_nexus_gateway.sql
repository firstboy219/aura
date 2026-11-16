-- ODI A4 (ADR-0073). Additive: three new tables. Nothing existing is altered.
-- Fallback authorizations: the ONLY way an external AI provider may serve cognition once NEXUS is primary (Blueprint §7). Each one is scoped by task,
-- purpose, data classes, providers, budget, call count and expiry, and carries the evidence that justified it.
CREATE TABLE fallback_authorizations (
  id TEXT PRIMARY KEY,
  task_id TEXT,
  purpose TEXT NOT NULL,
  trigger_code TEXT NOT NULL CHECK (trigger_code IN ('NEXUS_UNAVAILABLE','OWNER_DISABLED')),
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
CREATE INDEX fallback_active ON fallback_authorizations(state, expires_at);
-- Teacher Gate (ODI owner decision 2026-11-16): NEXUS may learn from an external AI only through AURA. Question and answer are kept (the question is
-- sanitized public text); the answer goes back to NEXUS as untrusted data.
CREATE TABLE teacher_requests (
  id TEXT PRIMARY KEY,
  gap TEXT,
  question TEXT NOT NULL,
  expected_form TEXT,
  data_class TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('denied','answered','failed')),
  deny_code TEXT,
  provider TEXT,
  model TEXT,
  answer TEXT,
  cost_usd REAL,
  audit_ref TEXT,
  created_at INTEGER NOT NULL);
-- Shadow mode: for the same call, what NEXUS did next to the legacy route (sizes and timing only, no content).
CREATE TABLE nexus_shadow (
  id TEXT PRIMARY KEY,
  task_id TEXT,
  purpose TEXT,
  ok INTEGER NOT NULL,
  error_code TEXT,
  nexus_ms INTEGER,
  legacy_provider TEXT,
  nexus_chars INTEGER,
  legacy_chars INTEGER,
  created_at INTEGER NOT NULL);
