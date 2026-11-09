# ADR-0018 — Persistent Agent Supervisor v1

Status: **Proposed** (owner decided P2→P3 without a pause, A-21). Date: 2026-11-09.

## Decision
- Waits are rows in `task_waits` (kind `event` | `time` | `tasks`), created by `wait.event` / `wait.until` steps or
  by delegation. The task moves to `WAITING` with a readable wait reason.
- The supervisor is a Core loop (2 s tick + wake on `POST /events`) using SQL only — no model calls while idle.
- A wake is one transaction: mark the wait satisfied *only if not yet satisfied*, append `WOKE`, move the task to
  `RUNNING`. A unique wait row plus that conditional update gives exactly-once wake across restarts.
- External events (`external_events`) are idempotent by `(key, dedup_id)`. Events received before the wait was
  created also satisfy it (no lost wake).
- Owner controls: pause/resume/cancel (P1), `POST /tasks/{id}/priority`; the scheduler orders by priority then age.
