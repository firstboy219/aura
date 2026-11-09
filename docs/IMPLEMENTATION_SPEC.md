# IMPLEMENTATION_SPEC.md — Phase 1, milestone by milestone

Scope: Blueprint E.2 only. One milestone = one commit/PR with a completion report (AGENTS.md). Each milestone's tests
must pass with `./dev test` before the next starts. Deterministic tests use a fake provider and a local fixture HTTP
server; real-provider runs are opt-in (`AURA_REAL_PROVIDER=1`).

Status 2026-11-09: M1–M9 done; real-provider RS-1 10/10 on the deployed Core (free routes, A-20).
Evidence: `benchmarks/reports/P1-2026-11-09.md`.

| M | Deliverable | Acceptance (tests) |
|---|---|---|
| M1 | `internal/config` (YAML strict, env overrides, secret-file checks); `internal/store` (modernc sqlite, migrations 0001 per DATA_MODEL, backup-before-migrate via `VACUUM INTO`, single-writer queue, read pool); `internal/task` state machine + event append + projection; startup recovery skeleton; `aura doctor` real checks | invalid config refuses start; secrets with wrong mode/symlink refused; SQLite ≥ 3.51.3 asserted; up-migration from empty; illegal transitions rejected; event append-only triggers; backup opens with `integrity_check ok`; concurrent writers do not hit `SQLITE_BUSY` |
| M2 | `internal/worker` supervisor (spawn `python -I -m aura_worker`, env allowlist, idle timeout 60 s, crash restart with backoff, SIGTERM→SIGKILL); JSON-RPC codec; contract tests Go + Python on `contracts/schemas` | worker env contains only allowlisted vars; crash mid-call → restart + task recoverable; oversize message refused; both sides reject schema-invalid messages |
| M3 | `internal/policy`: grant mint/verify (ADR-0006), trusted manifest, tiers A0/A2/A3, A1 denied, approvals with canonical hash, expiry, single use | out-of-scope request denied + audited; A1 denied; changing one parameter invalidates approval; expired/reused approval rejected (SEC-08) |
| M4 | `internal/budget`: reservation in one write transaction against task + daily totals, reconciliation, fail-closed unknown cost; kill switch (`POST /system/stop`, `aura stop --all`) | 50 concurrent reservations never exceed limits; `BUDGET_EXCEEDED` persisted; kill switch cancels an in-flight (fake, slow) provider call and stops the worker within target time (measured, reported) |
| M5 | `internal/gateway`: OpenRouter adapter, model registry (`model_tiers`), 3 tiers, bounded retry/backoff on 429/5xx/timeouts, fallback among configured routes, JSON-schema structured output, 1 repair attempt, usage/cost recording | fake provider: retry bounded, fallback taken, invalid output → repair → fail with reason; usage rows complete; key never in logs/events |
| M6 | `internal/tools`: `web.fetch` (SSRF checker shared with P4), `workspace.read`, `workspace.write` (atomic, hashed, idempotent) | SSRF matrix (private v4/v6, loopback, link-local, metadata, mapped IPv6, DNS change between resolve and dial, redirect to private, proxy env ignored, size/time caps); path matrix (traversal, absolute, symlink file/dir escape, atomic write, same key twice = one write) |
| M7 | Python worker: `task.plan` (one planning call, schema), `task.step` sequential, untrusted observation wrapping; Core deterministic completion check | RS-1 passes with fake provider; injected instruction in fixture page does not change tools/policy (P1 injection baseline) |
| M8 | `internal/api` REST per API_SPEC + CLI commands; slog JSON logs with redaction; `/system/health`, `/system/resources` | auth required everywhere except health; rate limit; structured errors; request IDs; OpenAPI conformance test of responses |
| M9 | Acceptance pack | 20× random kill/restart: zero duplicate idempotent writes, every task terminal/waiting/recoverable; secret scan (logs, events, worker environ); resource measurements on this host with co-resident load recorded; RS-1 ≥ 9/10 real-provider runs (opt-in, after owner provides key) |

Deployment after M9 (owner command only): `/opt/aura/{config.yaml,secrets/}` prepared, compose `command` switched to
`serve`, `docker compose -p aura up -d --build`.
