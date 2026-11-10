# ADR-0033 — Per-task recovery policy (`auto` / `manual` / `never`)

Status: **Proposed** (2026-11-10). Implements Blueprint §8.2: "Recovery policy: `auto`, `manual`, or `never`. Automatic recovery
is forbidden while an external write has an unknown outcome."

## Decision
- A task carries `recovery` in its scope: `auto` (default, stored as empty for older rows), `manual` or `never`. It is set at
  creation (`POST /tasks` `recovery`, CLI `--recovery`) and inherited by subagents. No migration is needed (scope is JSON).
- Startup recovery (ADR-0013 lease, ADR-0029 events) first resolves the started steps by side-effect class, as before.
  - Unknown external outcome: the task goes to `WAITING_APPROVAL` (`OUTCOME_UNKNOWN`) **under every policy**.
  - Otherwise, `auto` resumes the task. `manual` moves it to `PAUSED` (reason `interrupted_recovery_manual`), and the owner
    resumes it with the normal resume command. `never` moves it to `FAILED` (reason `interrupted_recovery_never`, canonical code
    `INTERRUPTED`) and revokes its grants.

## Consequences
The owner can keep sensitive or long tasks from restarting on their own after a crash. The default behaviour is unchanged.
Tests: `engine/recovery_test.go` (auto, manual → resume → completed, never → INTERRUPTED, invalid value refused).
