# ADR-0012 — Commercial boundary (contract only)

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision (contract)
Runtime stays deployment-neutral: one instance = one workspace boundary (A-10). The only runtime identity contract is
an `instance_id` (ULID generated on first start, stored in `schema_migrations` metadata) and `release_version`.
No billing, subscription, licensing, provisioning or Control Plane code, tables or endpoints in P0/P1.
