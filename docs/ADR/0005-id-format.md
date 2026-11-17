# ADR-0005 — Identifier format

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
**ULID** (26-char Crockford base32, `github.com/oklog/ulid/v2`, monotonic entropy per process) for every primary
ID (tasks, steps, events, approvals, grants, usage records). Stored as TEXT. Sortable by time, readable in CLI.
Event ordering inside a task uses an integer `seq` (unique per task), not the ULID.

## Alternatives
UUIDv7 — equally good; ULID chosen for shorter CLI display and existing schema regex in contracts.
