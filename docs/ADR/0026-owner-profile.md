# ADR-0026 — Owner Working Profile (P3)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
`profile_items`: statement, scope (`global` or a task category), provenance (`explicit` via API/CLI, or `inferred`
from a correction in task X), confidence, status (`confirmed` | `tentative` | `disabled` | `deleted`), version.
Explicit statements are `confirmed`. Inferred ones (proposed by reflection after an owner correction) are `tentative`:
shown to the planner only labelled as unconfirmed hints, never as rules, until the owner confirms them. Owner can
inspect, edit (new version), confirm, disable and delete. Scope is this workspace (instance); nothing is shared across
workspaces (OD-6).
