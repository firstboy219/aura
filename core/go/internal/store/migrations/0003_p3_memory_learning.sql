-- aura:foreign_keys_off
-- P3 + checkpoint v1.3.11: task states aligned with Blueprint §8.2 (COMPLETED, WAITING_EXTERNAL, SCHEDULED; INTERRUPTED and
-- RECOVERING are modeled as events, ADR-0029), canonical reason codes, memory (FTS5), skills, owner profile, routing
-- purposes and shadow decisions, triggers, watches, notifications, and the Limitation Ledger (Blueprint §8A.5).
CREATE TABLE tasks_new (
  id TEXT PRIMARY KEY, objective TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('CREATED','PLANNING','RUNNING','WAITING_APPROVAL','WAITING_INPUT','WAITING_EXTERNAL','SCHEDULED',
        'PAUSED','COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED')),
  reason TEXT, reason_code TEXT, wait_reason TEXT,
  parent_task_id TEXT REFERENCES tasks(id), root_task_id TEXT NOT NULL,
  agent_role TEXT NOT NULL DEFAULT 'main', depth INTEGER NOT NULL DEFAULT 0,
  lease_owner TEXT, lease_expires_at INTEGER,
  budget_json TEXT NOT NULL, scope_json TEXT NOT NULL DEFAULT '{}', config_hash TEXT NOT NULL,
  idempotency_key TEXT UNIQUE, started_at INTEGER,
  cost_reserved_usd REAL NOT NULL DEFAULT 0, cost_actual_usd REAL NOT NULL DEFAULT 0,
  last_seq INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 0, replans INTEGER NOT NULL DEFAULT 0, tier_override TEXT, task_class TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
INSERT INTO tasks_new (id, objective, state, reason, wait_reason, parent_task_id, root_task_id, agent_role, depth,
  lease_owner, lease_expires_at, budget_json, scope_json, config_hash, idempotency_key, started_at,
  cost_reserved_usd, cost_actual_usd, last_seq, version, priority, replans, tier_override, created_at, updated_at)
  SELECT id, objective,
    CASE state WHEN 'SUCCEEDED' THEN 'COMPLETED' WHEN 'WAITING' THEN 'WAITING_EXTERNAL' ELSE state END,
    reason, wait_reason, parent_task_id, root_task_id, agent_role, depth,
  lease_owner, lease_expires_at, budget_json, scope_json, config_hash, idempotency_key, started_at,
  cost_reserved_usd, cost_actual_usd, last_seq, version, priority, replans, tier_override, created_at, updated_at FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE INDEX tasks_state_created ON tasks(state, priority, created_at);
CREATE INDEX tasks_parent ON tasks(parent_task_id);
CREATE INDEX tasks_root ON tasks(root_task_id);
CREATE INDEX tasks_class ON tasks(task_class, state);

ALTER TABLE task_steps ADD COLUMN reason_code TEXT;


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
  content_sha256 TEXT NOT NULL, content TEXT NOT NULL DEFAULT '', checked_at INTEGER NOT NULL, PRIMARY KEY (trigger_id, source));
CREATE TABLE notifications (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('finding','info')),
  trigger_id TEXT REFERENCES triggers(id), task_id TEXT REFERENCES tasks(id),
  title TEXT NOT NULL, body_json TEXT NOT NULL, read_at INTEGER, created_at INTEGER NOT NULL);
CREATE INDEX notifications_unread ON notifications(read_at, created_at);


-- Limitation Ledger (Blueprint §8A.5): Core-owned, versioned, workspace-scoped.
CREATE TABLE limitations (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL CHECK (status IN ('open','diagnosing','remedy_proposed','remedy_testing','resolved','rolled_back','escalated','wont_fix')),
  category TEXT NOT NULL CHECK (category IN ('capability_gap','quality','cost','latency','reliability','resource','knowledge','safety',
        'external_dependency','permission_boundary')),
  task_class TEXT NOT NULL DEFAULT '', title TEXT NOT NULL,
  detected_by TEXT NOT NULL CHECK (detected_by IN ('verifier','self_model','runtime_monitor','owner_feedback','benchmark')),
  dedupe_key TEXT NOT NULL UNIQUE,
  evidence_json TEXT NOT NULL, impact_json TEXT NOT NULL DEFAULT '{}', diagnosis_json TEXT NOT NULL DEFAULT '{}',
  baseline_json TEXT NOT NULL DEFAULT '{}', result_json TEXT NOT NULL DEFAULT '{}', owner_note TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, closed_at INTEGER);
CREATE INDEX limitations_status ON limitations(status, category);
CREATE TABLE limitation_remedies (
  id TEXT PRIMARY KEY, limitation_id TEXT NOT NULL REFERENCES limitations(id),
  class TEXT NOT NULL CHECK (class IN ('R0','R1','R2','R3','R4','R5','R6')),
  version INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL CHECK (status IN ('proposed','testing','promoted','rolled_back','escalated','recorded')),
  description TEXT NOT NULL, artifact_json TEXT NOT NULL DEFAULT '{}',
  before_json TEXT NOT NULL DEFAULT '{}', after_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX remedies_lim ON limitation_remedies(limitation_id, status);
