# ADR-0008 — Event retention and audit/content separation

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- `task_events` (append-only, source of truth) store **metadata and redacted summaries**: type, step, tool, status,
  sizes, hashes, cost, durations, reason codes. Large or sensitive content (fetched pages, model outputs, file
  contents) is stored as content blobs in `checkpoints`/workspace with a hash referenced from the event.
- `audit_events` record security-relevant decisions (auth failures, capability denials, approvals, kill switch,
  config changes) and are retained ≥ 365 days; content blobs default to 30 days, configurable; events are kept for
  the task lifetime + 90 days. Deletion of content never deletes the audit record (it records `content_purged`).
- **P1 implementation:** a daily maintenance job deletes expired checkpoints of finished tasks and appends an internal
  `CONTENT_PURGED` event; owner result files in the workspace are never purged by it.
- A redaction filter runs on every event/log write: known secret values (loaded secrets), bearer/capability tokens and
  `Authorization` headers are replaced by `[REDACTED]`; the secret-scan test (D.1 P1) greps logs/events for them.
