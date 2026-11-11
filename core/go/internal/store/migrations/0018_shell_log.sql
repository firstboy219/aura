-- Server commands AURA ran or wanted to run in its shell sandbox (ADR-0065): the watcher's verdict and reasons, the
-- owner's approval when one was needed, the outcome, the output (credential-shaped strings redacted), the files moved
-- and the hosts reached through the egress proxy. Rows are never updated after finished_at is set.
CREATE TABLE shell_log (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  step_id TEXT,
  command TEXT NOT NULL,
  verdict TEXT NOT NULL CHECK (verdict IN ('safe','review','blocked')),
  reasons_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL CHECK (status IN ('refused','awaiting_approval','running','ran','timeout','error')),
  approval_id TEXT,
  exit_code INTEGER,
  duration_ms INTEGER,
  stdout TEXT,
  stderr TEXT,
  stdout_bytes INTEGER NOT NULL DEFAULT 0,
  stderr_bytes INTEGER NOT NULL DEFAULT 0,
  redacted INTEGER NOT NULL DEFAULT 0,
  files_in_json TEXT NOT NULL DEFAULT '[]',
  files_out_json TEXT NOT NULL DEFAULT '[]',
  hosts_json TEXT,
  egress_denied INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  created_at INTEGER NOT NULL,
  finished_at INTEGER);
CREATE INDEX shell_log_task ON shell_log(task_id, created_at);
CREATE INDEX shell_log_created ON shell_log(created_at);
