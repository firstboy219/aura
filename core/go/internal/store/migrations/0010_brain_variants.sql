-- L3 brain variants and AURA Lab (Blueprint D.1 P6, §8A.1 L3, §8A.5 R4, ADR-0048).
-- A variant is owner-approved guidance for one reasoning module (planner, replanner or verifier). It is drafted from a
-- Limitation Ledger entry, evaluated against the current version in AURA Lab, approved by the owner (bound to its sha256),
-- rolled out to a canary share of tasks, and promoted or rolled back automatically.
CREATE TABLE brain_variants (
  id TEXT PRIMARY KEY, remedy_id TEXT REFERENCES limitation_remedies(id), limitation_id TEXT REFERENCES limitations(id),
  module TEXT NOT NULL CHECK (module IN ('planner','replanner','verifier')),
  guidance TEXT NOT NULL, rationale TEXT NOT NULL DEFAULT '', sha256 TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('lab_testing','lab_failed','awaiting_approval','rejected','canary','active',
    'rolled_back','superseded')),
  lab_json TEXT NOT NULL DEFAULT '{}', rollout_json TEXT NOT NULL DEFAULT '{}',
  decided_by TEXT, decided_at INTEGER, canary_started_at INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX brain_variants_status ON brain_variants(status, module);
-- Each Lab run: which variant and arm (baseline = the current version, variant = with the candidate) a lab task belongs to.
CREATE TABLE lab_runs (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id), variant_id TEXT NOT NULL REFERENCES brain_variants(id),
  arm TEXT NOT NULL CHECK (arm IN ('baseline','variant')), kind TEXT NOT NULL CHECK (kind IN ('exposing','regression')),
  from_task_id TEXT NOT NULL, cleaned_at INTEGER);
CREATE INDEX lab_runs_variant ON lab_runs(variant_id);
