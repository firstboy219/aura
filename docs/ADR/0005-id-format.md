# ADR-0005 — Identifier format

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision
**ULID** (26-char Crockford base32, `github.com/oklog/ulid/v2`, monotonic entropy per process) for every primary
ID (tasks, steps, events, approvals, grants, usage records). Stored as TEXT. Sortable by time, readable in CLI.
Event ordering inside a task uses an integer `seq` (unique per task), not the ULID.

## Alternatives
UUIDv7 — equally good; ULID chosen for shorter CLI display and existing schema regex in contracts.
