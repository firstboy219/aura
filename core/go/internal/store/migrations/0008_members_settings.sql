-- Workspace Admin foundation (Blueprint D.1 P5, ADR-0043): members with roles and their own API tokens (stored by hash),
-- and owner-set runtime settings (e.g. the daily budget).
CREATE TABLE members (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 60),
  role TEXT NOT NULL CHECK (role IN ('viewer','operator','approver')),
  token_hash TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL, disabled_at INTEGER, last_used_at INTEGER);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL);
