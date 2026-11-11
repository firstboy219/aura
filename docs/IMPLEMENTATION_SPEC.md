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

---

# Phase 2 — Agent Loop + Subagents v1 + Persistent Supervisor + Console v1

Scope: Blueprint D.1 P2. Started 2026-11-09 after P1 approval (A-21). One milestone = one commit.

| M | Deliverable | Acceptance (tests) |
|---|---|---|
| P2-M1 | Migration 0002 (new task states `WAITING_INPUT`, `WAITING`; `priority`; per-step `plan_json`; tables `task_waits`, `task_inputs`, `shared_context`, `external_events`, `task_files`), migrator support for SQLite's documented table-rebuild procedure (foreign keys off around one migration), contracts for new methods | up-migration from 0001 with data preserved; FK check clean; old rows readable |
| P2-M2 | Agent loop: step failure → bounded replanning (`task.replan`, ≤ 2); after steps, deterministic checks then `task.verify` against completion criteria; failed verification → replan; plan review for complex plans; clarification question from planning → `WAITING_INPUT`; cascade tiers (fast for checks, standard for planning/writing, deep after repeated verification failure) | RS-2-style fake runs: 404 step recovered by replan; failed verification triggers exactly one replan; INT-05: ambiguous objective → one question, resumes after answer; plan review only above the complexity rule |
| P2-M3 | Tools `web.search` (Wikipedia keyless; Brave with key file), `data.read` (CSV/JSON summary with exact column stats), `calc` (exact rational arithmetic); provenance-bound dynamic arguments (a fetch URL not in the plan must appear in an earlier search result or fetched page link recorded by Core); task attachments (owner input files) | tool unit tests; dynamic URL without provenance denied; calc exactness; data.read stats recomputed by calc |
| P2-M4 | Persistent Supervisor v1: `wait.event` / `wait.until` steps and child-task waits; durable wake exactly once; event intake `POST /events`; reprioritize; idle supervisor uses no model calls | RS-10: wait → Core restart → event → exactly one wake, no duplicate write; idle CPU measured |
| P2-M5 | Subagents v1 (sequential): `agent.delegate` steps create child tasks with briefing, attenuated grant (subset), budget carve-out; Shared Task Context with provenance and trust labels; owner plan change reaches active children; cancel root cancels descendants; tree trace with rolled-up cost | RS-7 with fake provider; widening/overspend denied and audited |
| P2-M6 | MCP client adapter: owner-declared stdio MCP servers; only tools declared in config with Core-side tier and side-effect class are exposed; same grant/budget/approval rules | fake MCP server: A0 tool runs, A2 tool waits, undeclared tool denied |
| P2-M7 | Second provider: OpenAI-compatible adapter (any base URL + key file) as fallback profile for `fast` and `standard` | fake second provider takes over when OpenRouter fails |
| P2-M8 | SSE event stream, activity timeline endpoint, task files endpoint; Console TUI v1 (`aura console`): create task, live trace, approve A2 with full preview, answer questions, kill switch with confirmation, 80×24 | API tests; console model tests (update/view) |
| P2-M9 | Eval suite v1 (≥ 15 tasks) with runner and report (success, cost, latency, failures); P2 evidence pack | suite runs against the deployed Core |

---

# Phase 3 — Memory, Identity, Charter, Learning, Self-Model v1, L1 (Agent MVP)

Scope: Blueprint D.1 P3. Started 2026-11-09 directly after P2 (owner decision A-21). One milestone = one commit.
`ISLAMIC_CHARTER.md` is MISSING: the Charter *framework* and GOV-06 integrity are built; GOV-01…05 need the owner's
Charter text and an approved Quran/hadith source and stay **pending** (never invented).

| M | Deliverable | Acceptance (tests) |
|---|---|---|
| P3-M1 | Migration 0003; **memory** (`memories` + FTS5): kinds fact/lesson, provenance (task, step, source, evidence), workspace scope (instance id), privacy class, status (active/tentative/corrected/deleted) with version history; `memory.search` for planning context; `memory.propose` from reflection — claims are promoted only with evidence the Core finds in the task's output/observations | unsupported claim stays tentative; correction creates a new version; deletion tombstones content but keeps an audit record; private memories never reach prompt-logging routes |
| P3-M2 | **Identity** (Core-owned identity statement in every worker prompt; honesty statement §4) and **Charter framework**: owner Charter file pinned by sha256 in config, loaded read-only, injected as highest-priority rules; nothing (memory, skills, model output, workers, evolution) can modify it; proposals that touch policy/Charter are rejected | GOV-06 tests: memory/skill/model attempts to alter the Charter are rejected and audited; hash mismatch refuses start |
| P3-M3 | **Learning L1**: `task.reflect` after verified success proposes a lesson and/or a skill (reusable plan template with trigger terms); skills versioned (diff), disable/enable, rollback; planning receives matching active skills and lessons | RS-3 (fake provider): second related run reuses the skill and needs fewer model calls/steps; skill rollback restores the previous version |
| P3-M4 | **Owner Working Profile**: explicit preferences (confirmed) and inferred ones from owner corrections (tentative, never applied as rules); inspect, correct, disable, delete; versioned; scoped to this workspace (instance) | RS-11: tentative vs confirmed handling, correction versioning, second workspace (separate instance/DB) sees nothing |
| P3-M5 | **Self-Model v1** (measured capabilities, success, cost, latency, limitations from traces), **efficiency scorecard** (verified success, cost per successful task, latency percentiles, tokens, CPU/RAM; energy reported as unavailable), **trace-driven routing in shadow mode** (learned tier policy per call purpose, recorded next to the actual choice, never auto-promoted) | reports computed from fixtures; shadow decisions recorded; no live routing change |
| P3-M6 | **Scheduled triggers** (interval/daily) creating tasks under normal budget/permission checks; **Proactive Intelligence v1**: read-only watches on configured public sources with deterministic change detection (no model call when unchanged), findings with source, timestamp, observed change, confidence, relevance, suggested next step; owner notifications | RS-12: change reported, malicious instruction in the source not followed, no writes/messages possible; unchanged source costs 0 model calls |
| P3-M7 | **Console v2** panels (memory with provenance + correct/delete, skills diff/disable/rollback, self-model, profile, notifications); API/OpenAPI | console model tests |
| P3-M8 | Deploy, eval re-run, P3 evidence pack | — |


---
**Status 2026-11-09 (checkpoint v1.3.11):** P2 milestones M1–M8 and P3 milestones M1–M6 are implemented and tested; M9 (P2 eval) and
P3-M7 (Console v2 panels) are partial — see `docs/CONFORMANCE_v1.3.11.md` for the exact gaps and for the Ledger milestone added by the v1.3.11 package.

---
**Living canvas (2026-11-11, ADR-0060, owner instruction):** view language v2 (hero, timeline, progress, compare,
quote, form; `say`/`icon`/`span`/`emphasis`; scene), narrated replies, AURA-designed screens (`ui.design`,
`ui_screens`, migration 0015, `/api/v1/ui/*` with versions/rollback/reset), Core-computed home context, gateway
null-fill, Android client rebuilt on Compose with screenshot tests, web app renders the new blocks.
Tests: `internal/view` (sanitiser, screens), `internal/engine/screens_test.go`, `internal/gateway/fillnulls_test.go`,
Python `test_agent.py`, Android `ClientTest`/`CanvasTest`/`ScreensTest`. Not done: camera/OCR, phone control, music,
maps, live translation, prayer times, commute, money, habits (roadmap only).

---
**Workspace (2026-11-11, ADR-0061, owner instruction):** Android home is an empty workspace with a communication
panel; replies appear there block by block with narration. New blocks `figures` (Core-found Wikipedia pictures,
`media`), `note` (Core-kept notes, `/api/v1/notes`), `player` (plays the phone's own library); `links` as headline
cards; migration 0016; `GET /api/v1/media/{id}`. Tests: `internal/engine/workspace_test.go`, `internal/view`,
`internal/tools` (picture finder), Android `ClientTest` (parsing, `Tunes.pick`, captions) and `ScreensTest`
(workspace snapshots). Not done: camera/vision (button dimmed, says so), online streaming, background playback.
