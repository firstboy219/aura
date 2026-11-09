# ADR-0013 — Stable contracts for P2 supervisor, activity events and telemetry

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision (contracts only; no supervisor/scheduler/learning/profile code in P0/P1)
- **Task lease:** `tasks.lease_owner`, `tasks.lease_expires_at` columns exist from P1 (Core is the only owner in
  P1); startup recovery treats an expired lease as "recover from last checkpoint".
- **Wait reason:** `tasks.wait_reason` (nullable text) — used by `WAITING_APPROVAL` in P1, by `WAITING_INPUT` /
  dependency waits in P2.
- **Activity semantics:** every `task_events` row has `type`, `actor` (`core` | `worker` | `owner`), and
  `visibility` (`timeline` | `internal`); the P2 activity timeline is a projection of `timeline` events only.
- **Telemetry schema:** `usage_records` carry tier, provider, provider API model ID, route ID, input/output/
  reasoning tokens, estimated cost, reserved cost, actual cost, cost uncertainty flag, latency ms, cache hit,
  outcome; resource samples (Core RSS/CPU, worker RSS/CPU) every 30 s in memory, summarized per task. Energy is
  `unavailable` (no sensor), never estimated.
- **Owner-profile provenance** and **proactive research boundary** are recorded as P3 rules only: read-only,
  source+timestamp+confidence on every finding.
