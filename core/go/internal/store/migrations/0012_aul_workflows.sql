-- AUL workflows (Blueprint D.1 P8, ADR-0050): owner-registered, checked programs (source, IR and its sha256, declared
-- capabilities), and one row per run linking the task to the workflow version, its inputs and output.
CREATE TABLE aul_workflows (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, version INTEGER NOT NULL, source TEXT NOT NULL, ir_json TEXT NOT NULL,
  sha256 TEXT NOT NULL, requires_json TEXT NOT NULL, status TEXT NOT NULL CHECK (status IN ('active','superseded','disabled')),
  created_by TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE (name, version));
CREATE TABLE aul_runs (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id), workflow_id TEXT NOT NULL REFERENCES aul_workflows(id),
  inputs_json TEXT NOT NULL, output_json TEXT, calls INTEGER NOT NULL DEFAULT 0, finished_at INTEGER);
