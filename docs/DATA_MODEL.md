# DATA_MODEL.md — Phase 1 entities only (Blueprint §10)

Evidence level: **design-reviewed**. SQLite WAL (ADR-0001), ULID TEXT ids (ADR-0005), timestamps as INTEGER unix ms.
Migrations: `core/go/internal/store/migrations/NNNN_*.sql`, applied in order inside a transaction after an automatic
backup; `schema_migrations` records version, checksum, applied_at. Up-migrations are tested from empty and from every
previous version.

```sql
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL,
  applied_at INTEGER NOT NULL);
-- instance identity (ADR-0012) is stored as a row in schema_migrations metadata via version 0.

CREATE TABLE tasks (
  id TEXT PRIMARY KEY, objective TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('CREATED','PLANNING','RUNNING','WAITING_APPROVAL','PAUSED',
        'SUCCEEDED','FAILED','CANCELLED','BUDGET_EXCEEDED')),
  reason TEXT, wait_reason TEXT,
  parent_task_id TEXT REFERENCES tasks(id), root_task_id TEXT NOT NULL,       -- lineage (approved stable contract)
  agent_role TEXT NOT NULL DEFAULT 'main', depth INTEGER NOT NULL DEFAULT 0,
  lease_owner TEXT, lease_expires_at INTEGER,                                 -- ADR-0013
  budget_json TEXT NOT NULL, config_hash TEXT NOT NULL, idempotency_key TEXT UNIQUE,
  cost_reserved_usd REAL NOT NULL DEFAULT 0, cost_actual_usd REAL NOT NULL DEFAULT 0,
  last_seq INTEGER NOT NULL DEFAULT 0, version INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);

CREATE TABLE task_steps (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), step_no INTEGER NOT NULL,
  description TEXT NOT NULL, tool_id TEXT, side_effect TEXT NOT NULL
        CHECK (side_effect IN ('pure','read','write_idempotent','write_external')),
  state TEXT NOT NULL CHECK (state IN ('PENDING','STARTED','SUCCEEDED','FAILED','SKIPPED','WAITING_APPROVAL')),
  idempotency_key TEXT NOT NULL UNIQUE, outcome_ref TEXT, outcome_hash TEXT, reason TEXT,
  attempts INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  UNIQUE (task_id, step_no));

CREATE TABLE task_events (                      -- append-only source of truth
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), seq INTEGER NOT NULL,
  step_id TEXT, type TEXT NOT NULL, actor TEXT NOT NULL CHECK (actor IN ('core','worker','owner')),
  visibility TEXT NOT NULL DEFAULT 'timeline' CHECK (visibility IN ('timeline','internal')),
  data_json TEXT NOT NULL,                       -- redacted metadata only (ADR-0008)
  at INTEGER NOT NULL, UNIQUE (task_id, seq));
CREATE TRIGGER task_events_no_update BEFORE UPDATE ON task_events BEGIN SELECT RAISE(ABORT,'append-only'); END;
CREATE TRIGGER task_events_no_delete BEFORE DELETE ON task_events BEGIN SELECT RAISE(ABORT,'append-only'); END;

CREATE TABLE checkpoints (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), step_id TEXT,
  label TEXT, state_json TEXT NOT NULL, sha256 TEXT NOT NULL, created_at INTEGER NOT NULL,
  purge_after INTEGER);

CREATE TABLE capability_grants (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), parent_grant_id TEXT REFERENCES capability_grants(id),
  token_sha256 TEXT NOT NULL UNIQUE, worker_generation INTEGER NOT NULL, audience TEXT NOT NULL DEFAULT 'worker',
  scopes_json TEXT NOT NULL,                     -- tools, operations, domains, path prefix
  expires_at INTEGER NOT NULL, revoked_at INTEGER, created_at INTEGER NOT NULL);

CREATE TABLE approvals (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), step_id TEXT NOT NULL REFERENCES task_steps(id),
  grant_id TEXT NOT NULL REFERENCES capability_grants(id), tool_id TEXT NOT NULL, tool_version TEXT NOT NULL,
  side_effect TEXT NOT NULL, target TEXT NOT NULL, params_json TEXT NOT NULL, action_hash TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','expired','consumed','invalidated')),
  decided_by TEXT, decided_at INTEGER, expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL);

CREATE TABLE providers (id TEXT PRIMARY KEY, kind TEXT NOT NULL, base_url TEXT NOT NULL, key_ref TEXT NOT NULL,
  health TEXT NOT NULL DEFAULT 'unknown', updated_at INTEGER NOT NULL);           -- key_ref = file path, never the key

CREATE TABLE model_tiers (tier TEXT NOT NULL, priority INTEGER NOT NULL, provider_id TEXT NOT NULL REFERENCES providers(id),
  display_name TEXT NOT NULL, api_model_id TEXT NOT NULL, route_id TEXT NOT NULL,
  context_tokens INTEGER, structured_output INTEGER NOT NULL DEFAULT 0, tool_calling INTEGER NOT NULL DEFAULT 0,
  price_in_per_mtok REAL, price_out_per_mtok REAL, reasoning_effort TEXT, health TEXT NOT NULL DEFAULT 'unknown',
  verified_at INTEGER, PRIMARY KEY (tier, priority));

CREATE TABLE usage_records (id TEXT PRIMARY KEY, task_id TEXT REFERENCES tasks(id), step_id TEXT,
  tier TEXT, provider_id TEXT, api_model_id TEXT, route_id TEXT, request_id TEXT,
  input_tokens INTEGER, output_tokens INTEGER, reasoning_tokens INTEGER, cache_hit INTEGER,
  cost_estimated_usd REAL NOT NULL, cost_reserved_usd REAL NOT NULL, cost_actual_usd REAL,
  cost_uncertain INTEGER NOT NULL DEFAULT 0, latency_ms INTEGER, outcome TEXT NOT NULL, created_at INTEGER NOT NULL);

CREATE TABLE audit_events (id TEXT PRIMARY KEY, type TEXT NOT NULL, actor TEXT NOT NULL, task_id TEXT,
  request_id TEXT, data_json TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT,'append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit_events BEGIN SELECT RAISE(ABORT,'append-only'); END;
```

Not created in P1 (scope rule): memory, identity, connectors, workspaces/tenants, subscriptions, billing,
Shared Task Context (P2).
