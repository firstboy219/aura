-- ODI A8 (ADR-0077). Additive: one table. Self-engineering proposals and their approvals; the code itself is never changed by this table.
CREATE TABLE change_proposals (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  level TEXT NOT NULL CHECK (level IN ('L0','L1','L2','L3')),
  level_reasons_json TEXT NOT NULL,
  components_json TEXT NOT NULL,
  proposal_json TEXT NOT NULL,           -- symptom, evidence, root-cause confidence, risks, test plan, rollback plan, expected outcome
  diff_text TEXT,                        -- capped; what is proposed, kept for the audit
  diff_sha256 TEXT,
  weakening_json TEXT NOT NULL DEFAULT '[]',
  proposed_by TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('draft','awaiting_approval','approved','rejected','in_canary','deployed','rolled_back','accepted','blocked')),
  approved_by TEXT,
  approved_at INTEGER,
  decision_note TEXT,
  evidence_json TEXT NOT NULL DEFAULT '[]',   -- post-deployment evidence, appended per transition
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
CREATE INDEX change_status ON change_proposals(status, created_at);
