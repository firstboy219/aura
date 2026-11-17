# ADR-0022 — Event stream and Console TUI

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Stream:** Server-Sent Events at `GET /api/v1/stream` (bearer auth; optional `task_id`), emitting task events in
  ULID order with `Last-Event-ID` resume. Same data as `/tasks/{id}/events`.
- **Console:** `aura console`, built with Bubble Tea + Lip Gloss (MIT). It uses only the public API and stream; every
  console action has a CLI equivalent. Works in 80×24 over SSH.
