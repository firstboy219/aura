# CONFORMANCE_v1.3.11.md — AURA implementation vs the v1.3.11 owner package

Date: 2026-11-09 (checkpoint requested by the owner). Compared: `docs/MASTER_BLUEPRINT.md` v1.3.11 (sha256 `f2b59c9e…`),
`AGENTS.md`, `CLAUDE.md`, `CLAUDE_CODE_KICKOFF.md`, `docs/RESEARCH_BENCHMARK.md` v1.3, `docs/MODEL_ASSIGNMENT.md` v1.2,
`docs/ISLAMIC_CHARTER.md` v0.1 draft — all installed verbatim from the owner's package (the earlier MISSING placeholders and
the AGENTS/CLAUDE files written by Claude Code are replaced).

Evidence levels (Blueprint §21.2): everything marked ✔ is **implementation-verified** (automated tests run in this repository's
containers, plus the real-provider runs named below). Nothing is **production-proven**.

Legend: ✔ met · ◐ partial (what is missing is stated) · ✘ not met · — not applicable yet (later phase)

## 1. Governance rules (AGENTS.md)

| Rule | Status | Note |
|---|---|---|
| 1 Current phase only; sub-gates P2a→P2b→P3a→P3b need owner acceptance | ◐ | The owner chose "P2 then P3 without a pause" (A-21; Blueprint OD-9 allows P3a before P2b). All of P2 and most of P3 were built without the per-gate acceptance; P3b items (profile, shadow routing, scorecard, proactive watch) were built early. The owner has not accepted the sub-gates formally — this report is the basis for that decision. |
| 3, 4 Brain/Hands; Core is the only DB writer and secret holder | ✔ | Worker has no keys, no DB, no I/O except stdio; allowlisted env. |
| 5 No unrestricted execution | ✔ | No shell tool; no exec of model output; `calc` is a pure arithmetic parser. |
| 6 Untrusted means untrusted | ✔ | Observations wrapped as untrusted; memory, MCP results, search results labelled; hostile-worker and RS-12 tests. |
| 7 Honesty / evidence levels | ✔ | Reports label the evidence level; failures are reported (e.g. the first free-model run, the queue bug). |
| 9 Independence (§15) | ✘ → open | Code, data and runtime are independent. **But** the provider key in use is a copy of another product's key (A-04, PROPOSAL-0001). Needs an owner decision (give AURA its own key). Documentation was scrubbed of other-product references. |
| 10 Do not edit owner documents | ✔ | Installed byte-identical (hashes in ASSUMPTIONS); only the Commands section of AGENTS.md was edited (allowed). |
| 11, 23 L4 boundary; no path to modify Core/policy/Charter/harness | ✔ | Skills limited to manifest tools; protected-topic proposals rejected and audited; Charter read-only and hash-pinned; improvement loop cannot touch code. |
| 16 Workspace isolation | ✔ | One instance = one workspace (own DB); test shows profile/memory do not leak to another instance. |
| 17 API-first | ✔ | CLI and Console are clients of the public API. |
| 18 One Mind, Many Hands | ✔ | Grant ⊆ parent, budget carve-out + tree-wide cap, findings with trust labels, cancel cascades, only main task asks the owner. (Pause cascade ◐: cancel cascades; pause of a parent leaves a waiting child to finish.) |
| 19 Models are configuration | ✔ | Agent logic names tiers only; tier/display/API ID/route ID kept distinct. Build models used: Claude Opus 5.5 and Claude Sonnet 5.5 (this session). |
| 20 Never weaken a test | ✔ | Tests were only renamed for the Blueprint state names (`SUCCEEDED→COMPLETED`, `WAITING→WAITING_EXTERNAL/SCHEDULED`). |
| 21 Change freeze | ✔ | P1 accepted by the owner before new scope; new ideas go to `docs/PROPOSALS.md`. |
| 22 Operational lessons | see §3 | |

## 2. Blueprint mechanisms

| Area | Status | Detail / gap |
|---|---|---|
| §8.1 durable event-sourced tasks, idempotency, recovery | ✔ | 20 random SIGKILL/restart cycles: all tasks end terminal, 0 duplicate writes (repeated 3×). Unknown external-write outcome → owner approval, never auto-retry (MCP test). |
| §8.2 state machine | ◐ | Names aligned (`COMPLETED`, `WAITING_EXTERNAL`, `SCHEDULED`, `WAITING_INPUT`). `INTERRUPTED`/`RECOVERING` are modeled as events (ADR-0029). `INPUT_TIMEOUT` policy ✔ (ADR-0032: reminder, then fail or continue with stated assumptions). Per-task recovery policy `auto/manual/never` ✔ (ADR-0033; an unknown external outcome always waits for the owner); `BUDGET_EXCEEDED` kept as a terminal outcome (assumption, may fold into `FAILED`). Reason codes: every failed task and step carries a canonical code (`internal/taxonomy`). |
| §8.3 approvals A0/A2/A3, A1 denied, action hash | ✔ | Hash-bound, expiring, single-use. Device-bound approval signatures (v1.3.10) are P5 — not built. |
| §8.4 budget governor, kill switch | ✔ | Atomic reservations incl. tree-wide cap; kill switch cancels in-flight provider calls. Provider-side hard caps (OL-12) not configured (key has no limit set by AURA). |
| §8.5 gateway, cascade, structured output, reasoning effort | ✔ | Native/prompt structured output, one repair, cascade fast→standard→deep, second-provider adapter (fake-server tested only — no real second key). |
| §8.5.1 registry | ◐ | Routes verified against the catalog; health recorded; no automatic capability discovery beyond the catalog. |
| §8.5.2 trace-driven routing (P3b) | ◐ | Shadow mode only (recorded, never applied) — as specified. Not yet evaluated on a reproducible evaluation set (needs ≥ 20 samples per purpose). |
| §8.6 context engine | ✘ | Only hard size caps and truncation; **no per-tier prompt budgets or compaction**. |
| §8.7 verifier, plan review | ✔ | Evidence-quoted verification, bounded replan, review for complex plans, deep escalation. |
| §8.8 tools | ✔ | `web.fetch`, `web.search` (Wikipedia/Brave), `data.read`, `calc` (exact), `workspace.*`, MCP adapter. Browser automation is P4 (—). |
| §8.9 memory | ◐ | FTS5 memory with provenance, evidence gate, correction/deletion. **Embeddings/hybrid retrieval not built** (optional per Blueprint). |
| §8.10 learning L1 | ✔ | Lessons, evidence-gated facts, skills (versioned, diff, rollback). |
| §8.11 triggers | ◐ | Recurring schedules (interval, daily in workspace zone) ✔. Authenticated inbound webhooks ✔ (ADR-0034): `POST /webhooks/{name}` with HMAC-SHA256 over timestamp + body, ±5 min window, per-delivery dedup, 16 KiB cap, audited; each webhook raises one configured external event (wakes waiting tasks; cannot start or change a task). The API is loopback-only, so external senders need an owner-provided TLS reverse proxy. |
| §8.11.1 proactive intelligence | ✔ | Read-only watches of public sources, hash change detection (0 model calls when unchanged), findings with source/time/confidence/relevance/next step, injection test. Connected-app watches wait for P5. |
| §8.12 observability / retention | ◐ | Events, audit, redaction, daily purge of expired content ✔. CPU/RAM time-series sampling not built (scorecard reads point-in-time RSS); energy reported as unavailable. |
| §8.2.1 Persistent Supervisor | ✔ | Durable waits, exactly-once wake across restarts (RS-10), no model calls while idle. |
| §8A.3 Self-Model | ✔ | `/self`, `/self-model` from records, not from the model. |
| **§8A.5 Closed-loop self-improvement** | ✔ for P3a scope | See §4. |
| §8B subagents | ✔ | RS-7 on the fake provider and E11 on real free models. |
| §13A.3 Console | ◐ | Console v1 ✔ (live trace, full approval preview, kill switch, 80×24). Console v2 panels ✔: memory (view, delete with confirmation), skills (disable/enable, roll back with confirmation), self-model, Limitation Ledger, notifications (opening marks read); all fit 80×24 and spell out state. Not in the TUI yet: memory *correct*, skill diff view, Owner Working Profile, Budgets panel (all available in the CLI). |
| Charter framework | ◐ | Hash-pinned loading, injection as top-priority rules, mismatch → features disabled + security event, GOV-06 tests. **Charter v0.1 is an unapproved owner draft** (`[OWNER]` fields open): CH-R1…R8 rules are **not enforced**, no approved Quran/hadith corpus exists, so GOV-01…05 cannot be evaluated. The identity statement carries the Blueprint baseline (no fatwa, no invented citations). |
| §15 independence | ◐ | See rule 9. |

## 3. Operational lessons (§8.13)

| ID | Status | Note |
|---|---|---|
| OL-01 empty answers / reasoning | ✔ | Explicit reasoning setting per route; empty content retried once with reasoning off; recorded. |
| OL-02 raw tool-call markup | ✔ | Treated as failed attempt, audited. |
| OL-03 free endpoints | ◐ | **Deviation (owner choice, A-20):** free routes are first in each tier because OpenRouter credit is 0; they serve only tasks marked public. Circuit breaker marks failing routes; **no pre-enable probe call** yet (the catalog check is a probe without spend). |
| OL-04 orphaned runs / leases | ✔ | Leases, startup reconciliation, watch runs interrupted by a crash are failed. |
| OL-05 prompt size | ✔ | Prompt size per call in the trace; per-tier prompt budget `prompt_budget_chars` (fast 24k, standard 60k, deep 120k characters): the worker trims untrusted observation bodies (longest first, delimiters kept) to fit, and a call still over budget is flagged `over_budget` in `MODEL_CALLED`. Tool docs are sent only for granted tools. |
| OL-08 workspace time zone | ✔ | `timezone` (default Asia/Jakarta); budget day, schedules; 23:59–01:00 WIB tests. |
| OL-09 short replies | ✔ | A question and its answer travel together to planning; a deterministic interpreter reads short replies (ya/setuju/ok, tidak/jangan, maksudnya?/explain again) against that question: yes/no add no facts and grant nothing, clarify supplies no information; the planner never adds owner-confirmation steps (the Core asks for approvals). A model-based interpreter for longer replies is not built. |
| OL-10 agent must not grade itself | ✔ | No runtime path to tests/eval harness; verification needs evidence found by the Core. |
| OL-11 idempotent notifications per device | ✘ | Notifications exist; no device concept yet (P2b/P5). |
| OL-12 atomic reservations | ✔ | |

## 4. Closed-loop self-improvement (§8A.5) and SELF-01…07

| Element | Status | Evidence |
|---|---|---|
| Limitation Ledger (fields, statuses, owner view/add/dismiss/accept/reopen) | ✔ | `limitations`, `limitation_remedies`; `/limitations`; `aura limitations`; ADR-0030 |
| Detection from evidence with minimum samples; safety events immediately; one failure never opens one | ✔ | SELF-01 |
| Deterministic-first diagnosis, fixed taxonomy, model only below 0.6 confidence | ✔ | SELF-02 (≥ 80 % accuracy on 5 seeded causes: 5/5) |
| Remedy classes R0–R6; permission boundary → owner request, never widened | ✔ | SELF-03 |
| R2 skill remedy with re-test (exposing tasks + regression sample), promote beyond noise else roll back with reason | ✔ | SELF-04 (both branches) |
| Improvement budget: own cap, lowest priority, yields to owner tasks, paused by kill switch | ✔ | SELF-05 |
| Honest disclosure with measured rates | ✔ | SELF-06; `aura self` |
| R0 runtime self-healing (provider failover, circuit breaker, worker restart, recovery) | ✔ | SELF-07 + breaker tests + crash test |
| Monitoring closes with evidence (recovery) / reopens on regression | ✔ | `CloseRecovered` test; a regression opens a new entry |
| R1 (manual until P3b), R3 (P4), R4 (P6), R5 (proposal only) | — / ✔ | R1 is a proposal to the owner; R3/R4 not available by design |
| **Not proven** | | R2 has only been exercised on fixtures and the fake provider; **no real limitation has yet been remedied end to end on production traffic**. The ledger has found real limitations (below), but the two it found were Core defects (R5), fixed by code, not by the loop. |

First real findings of the ledger on production data (2026-11-09): tasks queued behind slow tasks lost their wall budget before
they started (`MAX_WALL_TIME`). Fixed in the Core (wall budget now starts when a task starts) and regression-tested.

## 5. Benchmarks (RESEARCH_BENCHMARK v1.3)

| Item | Status |
|---|---|
| RS-1 real provider ≥ 9/10 | ✔ 10/10 (free routes), report `benchmarks/reports/P1-2026-11-09.md` |
| RS-3, RS-7, RS-10, RS-11, RS-12, SELF-01…07 | ✔ deterministic fixtures (fake provider) — **not yet repeated with a real provider** |
| Eval suite v1 (≥ 15 tasks) on the real provider | ◐ 11/17 passed on free routes (n = 1 per task; 3 provider outages, 3 verifier rejections) — `benchmarks/reports/eval-suite_v1-20261109T215207.md`; not a stable pass rate |
| RS-2, RS-4…RS-6, RS-8, RS-9 | — / ✘ RS-2 as specified (≥ 4 sources + A2) not run; RS-4/6/8/9 are P4/P5 |
| §5.6 Win conditions vs Hermes | ✘ **No claim possible**: Hermes is not installed or run, the owner has not approved the thresholds (OD-11), the P1 baseline exists only on free models. Capabilities Hermes has and AURA lacks are not yet listed (claim rule 3). |

## 6. Required next decisions for the owner

1. **Key independence (A-04 / PROPOSAL-0001):** give AURA its own provider key.
2. **Sub-gates:** formally accept (or reject) P2a/P2b/P3a/P3b based on this report; the gaps above are the open items.
3. **Charter:** complete and approve `ISLAMIC_CHARTER.md` (rules, approved sources, harmful-action list) so CH-R1…R8 and GOV-01…05 can be built and evaluated.
4. **OD-11:** approve the win-condition thresholds after a P1 baseline on the intended (paid) model profile.
5. **Credit:** add OpenRouter credit so private tasks and the paid profile (Haiku/Sonnet/Opus 5.5 per MODEL_ASSIGNMENT) can be exercised; until then AURA works on free, public-only routes.
