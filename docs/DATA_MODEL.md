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

Not created in P1 (scope rule): memory, identity, connectors, workspaces/tenants, subscriptions, billing,
Shared Task Context (P2).
