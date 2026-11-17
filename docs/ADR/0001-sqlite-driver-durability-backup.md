# ADR-0001 — SQLite driver, embedded version, WAL durability, checkpoint and backup

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Context
Blueprint §6, §10: pure-Go driver; verify embedded SQLite ≥ 3.51.3 (WAL-reset fix), FTS5, WAL, backup API; Go Core is
the only writer; single-writer queue; never back up by copying the main file while WAL is active.

## Facts verified in P0 (probe run 2026-11-09, golang:1.26-bookworm)
- `modernc.org/sqlite` **v1.62.0** (latest at probe time; requires Go ≥ 1.26), `modernc.org/libc` v1.80.1.
- `select sqlite_version()` → **3.54.1** (≥ 3.51.3 ✔). Verified at runtime, not inferred from the module version.
- `pragma journal_mode=WAL` → `wal` ✔. `create virtual table … using fts5` succeeds; compile options include
  `ENABLE_FTS5`, `DEFAULT_WAL_SYNCHRONOUS=2`, `DEFAULT_WAL_AUTOCHECKPOINT=1000`, `THREADSAFE=1`.

## Decision
1. Driver `modernc.org/sqlite` v1.62.0 pinned in go.mod; `aura doctor` prints `sqlite_version()` and FTS5/WAL
   status at every start, and a test fails the build if the version is < 3.51.3.
2. Connections: one **write connection** owned by a writer goroutine (serialized queue, short transactions, batched
   where safe); a read pool (default 4) opened with `query_only=1`. `busy_timeout=5000`, `foreign_keys=ON`.
3. Durability: `journal_mode=WAL`, `synchronous=FULL` (events are the source of truth; durability over latency until
   P1 measurements justify NORMAL). `wal_autocheckpoint=1000` pages plus a manual `PASSIVE` checkpoint every 60 s
   and a `TRUNCATE` checkpoint when WAL > 64 MiB; WAL size and last checkpoint are reported by
   `/system/resources`. Disk pressure: if free space on the data volume < 1 GiB, Core refuses new tasks
   (`disk_pressure`) and keeps running tasks' state durable.
4. Backup: `VACUUM INTO '<path>'` (consistent online snapshot) — or the driver's backup API if P1 proves it — into
   `/var/lib/aura/backups/`, before every migration and daily; each backup is opened and checked with
   `pragma integrity_check` (automated restore verification).
   **Amended in P1:** the daily backup is additionally verified by a restore drill (`store.RestoreDrill`: copy into
   a fresh directory, open through the normal startup path, integrity check, count tasks); failures are logged as errors. Raw copies of `aura.db` alone are forbidden.
5. Database files live on the local ext4 Docker volume `aura-data`, never a network filesystem.

## Consequences
Re-check SQLite advisories and the embedded version before each release (§21.4). Changing the driver or SQLite
version triggers the compatibility/regression suite.
