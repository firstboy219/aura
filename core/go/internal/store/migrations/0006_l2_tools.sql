-- L2 tools (Blueprint 8A.1, D.1 P4, ADR-0038): AURA-built pure tools that run in the WASM sandbox, registered only after
-- static checks, sandbox tests and benchmark, and an owner approval bound to the source hash.
CREATE TABLE l2_tools (
  id TEXT PRIMARY KEY, name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 40), version INTEGER NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('awaiting_approval','active','rejected','disabled','superseded')),
  description TEXT NOT NULL, input_schema_json TEXT NOT NULL, source TEXT NOT NULL, source_sha256 TEXT NOT NULL,
  tests_json TEXT NOT NULL, report_json TEXT NOT NULL,
  limitation_id TEXT REFERENCES limitations(id), remedy_id TEXT,
  decided_by TEXT, decided_at INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  UNIQUE (name, version));
CREATE INDEX l2_tools_status ON l2_tools(status, name);
