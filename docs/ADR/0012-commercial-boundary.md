# ADR-0012 — Commercial boundary (contract only)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision (contract)
Runtime stays deployment-neutral: one instance = one workspace boundary (A-10). The only runtime identity contract is
an `instance_id` (ULID generated on first start) and `release_version`.
**Amended in P1:** the instance ID is stored in a one-row `instance` table rather than overloading `schema_migrations`.
No billing, subscription, licensing, provisioning or Control Plane code, tables or endpoints in P0/P1.
