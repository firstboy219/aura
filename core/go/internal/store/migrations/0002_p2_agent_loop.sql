-- aura:foreign_keys_off
-- P2: new task states (WAITING_INPUT, WAITING) need a rebuilt tasks table (SQLite cannot alter a CHECK).
-- Procedure: https://www.sqlite.org/lang_altertable.html#otheralter (foreign keys off, rebuild, foreign_key_check).
CREATE TABLE tasks_new (
  id TEXT PRIMARY KEY, objective TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('CREATED','PLANNING','RUNNING','WAITING_APPROVAL','WAITING_INPUT','WAITING','PAUSED',
        'SUCCEEDED','FAILED','CANCELLED','BUDGET_EXCEEDED')),
  reason TEXT, wait_reason TEXT,
  parent_task_id TEXT REFERENCES tasks(id), root_task_id TEXT NOT NULL,
  agent_role TEXT NOT NULL DEFAULT 'main', depth INTEGER NOT NULL DEFAULT 0,
  lease_owner TEXT, lease_expires_at INTEGER,
  budget_json TEXT NOT NULL, scope_json TEXT NOT NULL DEFAULT '{}', config_hash TEXT NOT NULL,
  idempotency_key TEXT UNIQUE, started_at INTEGER,
  cost_reserved_usd REAL NOT NULL DEFAULT 0, cost_actual_usd REAL NOT NULL DEFAULT 0,
  last_seq INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 0, replans INTEGER NOT NULL DEFAULT 0, tier_override TEXT,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
INSERT INTO tasks_new (id, objective, state, reason, wait_reason, parent_task_id, root_task_id, agent_role, depth,
  lease_owner, lease_expires_at, budget_json, scope_json, config_hash, idempotency_key, started_at,
  cost_reserved_usd, cost_actual_usd, last_seq, version, created_at, updated_at)
  SELECT id, objective, state, reason, wait_reason, parent_task_id, root_task_id, agent_role, depth,
  lease_owner, lease_expires_at, budget_json, scope_json, config_hash, idempotency_key, started_at,
  cost_reserved_usd, cost_actual_usd, last_seq, version, created_at, updated_at FROM tasks;
DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;
CREATE INDEX tasks_state_created ON tasks(state, priority, created_at);
CREATE INDEX tasks_parent ON tasks(parent_task_id);
CREATE INDEX tasks_root ON tasks(root_task_id);

-- Planned arguments per step (replanning appends steps; dynamic arguments are checked against provenance).
ALTER TABLE task_steps ADD COLUMN plan_json TEXT;

-- Persistent supervisor waits (ADR-0018).
CREATE TABLE task_waits (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), step_id TEXT,
  kind TEXT NOT NULL CHECK (kind IN ('event','time','tasks')),
  key TEXT, until_at INTEGER, created_at INTEGER NOT NULL, satisfied_at INTEGER, satisfied_by TEXT);
CREATE INDEX waits_open ON task_waits(satisfied_at, kind);
CREATE INDEX waits_task ON task_waits(task_id);

-- Clarification questions (WAITING_INPUT).
CREATE TABLE task_inputs (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), step_id TEXT,
  question TEXT NOT NULL, answer TEXT,
  status TEXT NOT NULL CHECK (status IN ('pending','answered','cancelled')),
  created_at INTEGER NOT NULL, answered_at INTEGER);
CREATE INDEX inputs_task ON task_inputs(task_id, status);

-- Shared Task Context per root task (ADR-0019).
CREATE TABLE shared_context (
  id TEXT PRIMARY KEY, root_task_id TEXT NOT NULL REFERENCES tasks(id), author_task_id TEXT REFERENCES tasks(id),
  kind TEXT NOT NULL CHECK (kind IN ('objective','finding','progress','question','plan_change','owner_correction')),
  trust TEXT NOT NULL CHECK (trust IN ('owner','core_verified','untrusted')),
  content TEXT NOT NULL, provenance_json TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL);
CREATE INDEX ctx_root ON shared_context(root_task_id, id);

-- External dependency events (ADR-0018), idempotent by (key, dedup_id).
CREATE TABLE external_events (
  id TEXT PRIMARY KEY, key TEXT NOT NULL, dedup_id TEXT NOT NULL, payload_json TEXT NOT NULL DEFAULT '{}',
  received_at INTEGER NOT NULL, UNIQUE (key, dedup_id));
CREATE INDEX ext_key ON external_events(key, received_at);
