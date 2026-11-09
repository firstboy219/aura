-- P3: memory (FTS5), skills, owner profile, routing purposes and shadow decisions, triggers, watches, notifications.

CREATE TABLE memories (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('fact','lesson')),
  content TEXT NOT NULL,
  privacy TEXT NOT NULL CHECK (privacy IN ('public','private')),
  status TEXT NOT NULL CHECK (status IN ('active','tentative','superseded','deleted')),
  version INTEGER NOT NULL DEFAULT 1, supersedes TEXT REFERENCES memories(id),
  source_task_id TEXT REFERENCES tasks(id), provenance_json TEXT NOT NULL DEFAULT '{}',
  workspace_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX memories_status ON memories(status, kind);
CREATE VIRTUAL TABLE memories_fts USING fts5(content, content='memories', content_rowid='rowid', tokenize='unicode61');
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, content) VALUES (new.rowid, new.content); END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, content) VALUES ('delete', old.rowid, old.content); END;
CREATE TRIGGER memories_au AFTER UPDATE OF content ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, content) VALUES ('delete', old.rowid, old.content);
  INSERT INTO memories_fts(rowid, content) VALUES (new.rowid, new.content); END;

CREATE TABLE skills (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL CHECK (status IN ('active','disabled')),
  current_version INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE skill_versions (
  skill_id TEXT NOT NULL REFERENCES skills(id), version INTEGER NOT NULL,
  triggers TEXT NOT NULL, template_json TEXT NOT NULL, notes TEXT NOT NULL DEFAULT '',
  source_task_id TEXT REFERENCES tasks(id), created_at INTEGER NOT NULL,
  PRIMARY KEY (skill_id, version));
CREATE VIRTUAL TABLE skills_fts USING fts5(skill_id UNINDEXED, text, tokenize='unicode61');

CREATE TABLE profile_items (
  id TEXT PRIMARY KEY, statement TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'global',
  provenance TEXT NOT NULL CHECK (provenance IN ('explicit','inferred')),
  source_task_id TEXT REFERENCES tasks(id), confidence REAL NOT NULL DEFAULT 1,
  status TEXT NOT NULL CHECK (status IN ('confirmed','tentative','disabled','deleted','superseded')),
  version INTEGER NOT NULL DEFAULT 1, supersedes TEXT REFERENCES profile_items(id),
  workspace_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);

ALTER TABLE usage_records ADD COLUMN purpose TEXT;
CREATE TABLE routing_shadow (
  id TEXT PRIMARY KEY, usage_id TEXT REFERENCES usage_records(id), purpose TEXT NOT NULL,
  actual_tier TEXT NOT NULL, shadow_tier TEXT NOT NULL, policy_version TEXT NOT NULL, created_at INTEGER NOT NULL);

CREATE TABLE triggers (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL CHECK (kind IN ('task','watch')),
  interval_seconds INTEGER, daily_at TEXT,
  objective TEXT NOT NULL, spec_json TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1, next_run_at INTEGER NOT NULL, last_run_at INTEGER, last_task_id TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE watch_state (
  trigger_id TEXT NOT NULL REFERENCES triggers(id), source TEXT NOT NULL,
  content_sha256 TEXT NOT NULL, checked_at INTEGER NOT NULL, PRIMARY KEY (trigger_id, source));
CREATE TABLE notifications (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('finding','info')),
  trigger_id TEXT REFERENCES triggers(id), task_id TEXT REFERENCES tasks(id),
  title TEXT NOT NULL, body_json TEXT NOT NULL, read_at INTEGER, created_at INTEGER NOT NULL);
CREATE INDEX notifications_unread ON notifications(read_at, created_at);
