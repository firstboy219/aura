# DATA_MODEL.md — Phase 1 entities only (Blueprint §10)

Evidence level: **design-reviewed**. SQLite WAL (ADR-0001), ULID TEXT ids (ADR-0005), timestamps as INTEGER unix ms.
Migrations: `core/go/internal/store/migrations/NNNN_*.sql`, applied in order inside a transaction after an automatic
backup; `schema_migrations` records version, checksum, applied_at. Up-migrations are tested from empty and from every
previous version.

The schema is `core/go/internal/store/migrations/0001_init.sql` (single source of truth; summary below).
`schema_migrations(version, name, checksum, applied_at)` is created by the migrator. `instance` is a one-row table
holding the instance ID (ADR-0012, amended: a dedicated row instead of overloading `schema_migrations`).

| Table | Purpose |
|---|---|
| tasks | projection of task state; lineage fields `parent_task_id`, `root_task_id`, `agent_role`, `depth`; lease and wait reason (ADR-0013) |
| task_steps | planned steps, side-effect class from manifest, idempotency key, outcome ref/hash, attempts |
| task_events | append-only event log (UPDATE/DELETE abort via triggers), `actor`, `visibility` |
| checkpoints | plan, observations, write-ahead tool intents (content blobs, sha256, purge_after) |
| capability_grants | sha256 of opaque token, worker generation, scopes, expiry, revocation, parent grant |
| approvals | canonical action hash, exact parameters, expiry, single-use status |
| providers, model_tiers | provider registry (key *path* only) and verified tier routes with price/capabilities |
| usage_records | reservation, actual cost, tokens, route, latency, outcome per provider call |
| audit_events | append-only security decisions |
| instance | instance identity |

### Phase 2 additions (migration `0002_p2_agent_loop.sql`)
| Change | Purpose |
|---|---|
| tasks rebuilt | states `WAITING_INPUT`, `WAITING`; columns `priority`, `replans`, `tier_override` (SQLite table-rebuild procedure with foreign keys off and `foreign_key_check`) |
| task_steps.plan_json | planned arguments per step (replanning, provenance checks) |
| task_waits | supervisor waits: event / time / child tasks; exactly-once wake (ADR-0018) |
| channel_deliveries | owner notifications delivered per channel, once each (0009, ADR-0044, OL-11) |
| brain_variants | L3 brain variants: guidance for one reasoning module, Lab report, owner decision, canary rollout (0010, ADR-0048) |
| lab_runs | AURA Lab runs: lab task, variant, arm (baseline/variant), kind, cleanup time (0010, ADR-0048) |
| members, settings | workspace members with roles viewer/operator/approver and their own API tokens (by hash); owner runtime settings such as the daily budget (0008, ADR-0043) |
| web_owner, web_sessions, web_setup | Web App owner: argon2id password hash, TOTP replay step, lockout; sessions by hash with CSRF token; one-time setup links by hash (0007, ADR-0041) |
| secrets | encrypted secret values: AES-256-GCM ciphertext + nonce, key id; never plaintext (0005, ADR-0036) |
| task_inputs | clarification questions and owner answers; status pending/answered/cancelled/timed_out, `reminded_at` (0004, ADR-0032) |
| shared_context | Shared Task Context per root task with trust and provenance (ADR-0019) |
| external_events | idempotent external dependency events `(key, dedup_id)` |

Not created in P1 (scope rule): memory, identity, connectors, workspaces/tenants, subscriptions, billing,
Shared Task Context (P2).

### Phase 3 additions and checkpoint v1.3.11 (migration `0003_p3_memory_learning.sql`)
| Change | Purpose |
|---|---|
| tasks rebuilt | state names per Blueprint §8.2 (`COMPLETED`, `WAITING_EXTERNAL`, `SCHEDULED`, `WAITING_INPUT`); `reason_code` (canonical taxonomy), `task_class` |
| task_steps.reason_code | canonical failure code on every failed step |
| memories (+ memories_fts) | memory with provenance, privacy, status, versions; FTS5 kept in sync by triggers |
| skills, skill_versions (+ skills_fts) | versioned L1 skills |
| profile_items | Owner Working Profile (explicit/inferred, confirmed/tentative, versions) |
| usage_records.purpose, routing_shadow | call purpose; shadow routing decisions (never applied) |
| triggers, watch_state, notifications | schedules, read-only watches (content hash + last content), owner notifications |
| limitations, limitation_remedies | Limitation Ledger (Blueprint §8A.5) |
