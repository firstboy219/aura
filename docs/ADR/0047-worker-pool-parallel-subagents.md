# ADR-0047 — Worker pool and parallel subagents

Status: **Proposed** (P6, 2026-11-10). Supersedes the single-worker limit of ADR-0003 for P6+. Implements D.1 P6 "parallel
subagents under one parent with budget carving and cascade".

## Decision
- **Pool:** `worker.max_concurrent_tasks` (1..4) tasks are driven at once, each by its own worker process. A worker serves one
  task at a time, so worker state is never shared between tasks.
  - `worker.max_workers` follows the same range.
  - The isolated worker host (`aura worker-host --max-workers N`) accepts N connections. N is the number of concurrent tasks
    plus one, for restarts.
- **Restart accounting:** all workers draw generation numbers from one counter (`worker.Options.GenSource`), so a stale reply
  from a restarted worker can never be taken as the reply of another worker.
- **Scheduling:** the engine tracks `driving[taskID]` and one supervisor per driven task.
  - The scheduler picks the next runnable task that is not already being driven (`task.RunnableExcept`).
  - Interrupt, pause and stop act on the driving map.
  - Stop kills every supervisor.
- **Parallel subagents:**
  - Consecutive `delegate` steps in a plan are started together.
  - The parent parks as `Waiting` (`subagents:N`) until the children finish, then resumes with their findings.
  - Children are ordinary tasks: they carry carved budgets and subset grants, and cancel or pause cascades to them.
- **Single writer stays:** all database writes still go through the Core's single writer, so parallel tasks do not change the
  SQLite model.
- **Self-improvement cycles** are serialized (one at a time), so parallel tasks cannot start duplicate cycles.

## Resources
Production starts at 2 concurrent tasks:
- the worker container has mem_limit 768m (it was 512m) and runs `--max-workers 3`;
- the host has 15.4 GiB with about 8.6 GiB available while JARVIS runs.

## Evidence
`engine/parallel_test.go` runs two delegated children at the same time (overlap observed through a barrier in the fake model).
It checks that the parent waits and then completes with both findings, and that stop cascades. The full suite passes with the
pool enabled.

## Limits
- Concurrency above 2 has not been measured on this host.
- Budget reservations are atomic, but parallel children of one parent can reach the parent's limit sooner. In that case they
  are refused (fail-closed); they do not overspend.
