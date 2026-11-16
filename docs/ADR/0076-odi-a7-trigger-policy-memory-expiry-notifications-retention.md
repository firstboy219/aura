# ADR-0076 — ODI A7: trigger policy, memory expiry, notification hygiene, retention report

Status: **Proposed** — awaiting owner approval · Date: 2026-11-16 · Evidence: engine tests (implementation-verified)

## Context
Blueprint v1.1 §14 and §17. A0/A7 audit: events are append-only with a per-task sequence and external events are de-duplicated (AT-06 met); a trigger claims its slot before running and computes the next slot from "now", so a missed slot runs once and never as a burst (already compliant); memories carry provenance, status, version and lineage; but there was no overlap policy, quiet hours, trigger expiry, memory expiry, notification snooze/acknowledgement or de-duplication, and no retention view.

## Decision (migration 0022: nullable columns and one trigger; no row rewritten)
1. **Trigger policy** (optional `spec.policy`; none = unchanged behaviour): `overlap: skip` (skip the slot while the previous run is still going), `quiet_from/quiet_to` in the workspace zone (a slot inside the window waits for its end and is not lost), `expires_at`, `max_runs` (task triggers), `missed_grace_seconds` (a slot later than that is skipped, not run late), `jitter_seconds`. Each decision that skips or disables is audited (`TRIGGER_SKIPPED/EXPIRED/COMPLETED`); validated at creation.
2. **Memory expiry**: `memories.expires_at` (owner sets it with `expires_in_days` on `POST /memories` or `POST /memories/{id}/expiry`). An expired memory is never retrieved as a fact (checked at query time, before the tick), and the maintenance tick retires it: status `deleted`, row and lineage kept, audited (`MEMORY_EXPIRED`). Nothing is erased.
3. **Notifications**: `urgency`, `snoozed_until`, `acked_at`, `expires_at`; `POST /notifications/{id}/snooze|ack`; the list hides snoozed and expired ones; a SQLite trigger drops a notification that repeats an **unread** one (same kind, title, trigger and task) within 24 h, so there are no duplicate alerts without touching the many insert sites.
4. **Retention report** (`GET /admin/retention`): a dry run of the owner's proposal (`odi.retention` setting: days for read notifications, shell log, retired memories, finished approvals). **Nothing is deleted**; enforcing retention is a destructive change that needs the owner's explicit approval of the numbers and is not built. The audit log and task events are never part of a proposal.

## Not done
Event streaming/WebSocket ordering guarantees beyond the existing per-task sequence; legal-hold and export workflows; scoped deletion with propagation to derived data (AURA holds no derived copies of memory content beyond the FTS index, which follows the row).

## Rollback
Revert the commit; the columns and the trigger can stay (the trigger can be dropped with `DROP TRIGGER notifications_no_duplicate_unread`).
