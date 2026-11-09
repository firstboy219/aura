# AURA — Claude Code Kickoff (Phase 0)

> Commit these files to the new **private** `aura` repository first: `CLAUDE.md`, `AGENTS.md`, `CLAUDE_CODE_KICKOFF.md`, `docs/MASTER_BLUEPRINT.md` (v1.3.11), `docs/ISLAMIC_CHARTER.md`, `docs/RESEARCH_BENCHMARK.md` (v1.3), `docs/MODEL_ASSIGNMENT.md` (v1.2). Then start Claude Code in the repository, switch to Plan Mode, select **Claude Opus 5.5** (`MODEL_ASSIGNMENT.md` Section 3), and paste the text below.

---

You are the Lead Developer and Principal Implementation Engineer for **AURA (Autonomous Universal Runtime Agent)**. The human Product Owner/Architect is the final authority.

## Context

- Product intent, architecture, the canonical roadmap (P0–P9 with sub-gates P2a/P2b, P3a/P3b, P5a/P5b), and the Phase 0/1 scope are in `docs/MASTER_BLUEPRINT.md` v1.3.11. Use its Section 0 reading guide.
- Your rules are in `AGENTS.md` (loaded by `CLAUDE.md`). Follow the Assumption Protocol and Stop Conditions strictly.
- AURA is an **independent product** (Blueprint Section 15). It has no dependency on any other product.
- AURA is **proprietary and closed-source**; the repository is private.
- Evaluation: `docs/RESEARCH_BENCHMARK.md` v1.3. Models: `docs/MODEL_ASSIGNMENT.md` v1.2. Charter: `docs/ISLAMIC_CHARTER.md` (owner draft, read-only). All owner-controlled.
- The blueprint is frozen for new scope until P1 is accepted. Put new ideas in `docs/PROPOSALS.md`.
- Phase 1 must not block later capabilities: the task model, capability grants, tool manifest, event log, and trace schema must support subagents (P2b), the Persistent Supervisor and `SCHEDULED`/`WAITING_INPUT` states (P2a/P2b), browser tools (P4), OCA live sessions (P5), the model registry, and the Limitation Ledger (P3a), without redesign. Every failed step in P1 records a reason code from the failure taxonomy. Phase 1 builds none of them.

## Your task: Phase 0 only

Produce exactly the deliverables in Blueprint **Part E.1**, in one pull request:

1. Inspect the repository and environment (OS, Go and Python versions) and record facts, including the committed versions of all owner documents (Blueprint E.1 reconciliation gate). Stop if any differs from the list above.
2. `docs/ASSUMPTIONS.md` using the Assumption Protocol.
3. ADRs covering at least (Blueprint Part D, P0 acceptance):
   - SQLite driver, the embedded SQLite version (≥ 3.51.3 or a verified backport), FTS5, WAL durability (`synchronous`, checkpoint policy, maximum WAL growth), and online backup/restore
   - config format; Go–Python transport (JSON-RPC over stdio unless justified otherwise); Python tooling; ID format
   - capability grant model (opaque token + Core-side grant registry) and approval binding to a canonical action hash
   - worker scratch/isolation boundary for P1
   - event and content retention
   - representation of `INTERRUPTED`/`RECOVERING`, with reserved names for `WAITING_INPUT` and `SCHEDULED`
   - benchmark execution environment
   - browser egress boundary (contract only)
   - OCA audio transport, provider, turn-management, privacy, and voice-approval contracts (contract only)
   - task lease and supervisor contracts, activity-event semantics, trace/telemetry schema, owner-profile provenance rules, the limitation-ledger record schema and failure taxonomy (Section 8A.5), and the read-only proactive-research boundary (contracts only)
   - future commercial boundary (Control Plane vs Runtime Data Plane), contract level only
4. `docs/ARCHITECTURE.md`: Phase 1 module boundaries in the Go Core and the Python worker, plus a Mermaid sequence diagram of the Brain/Hands flow for RS-1.
5. `docs/SECURITY_MODEL.md`: trust boundaries, capability grants, approval tiers and approval integrity, secret handling, SSRF and path-safety baseline, the P1 threat model, and an explicit list of what is **not** hardened in P1.
6. `docs/DATA_MODEL.md`: Phase 1 entities only (Blueprint Section 10) with task lineage fields, event types, the allowed state-transition table, and migration strategy.
7. `contracts/api/openapi.yaml`, `docs/API_SPEC.md`, and `contracts/schemas/` for the Phase 1 endpoints and JSON-RPC methods.
8. `docs/IMPLEMENTATION_SPEC.md`: Phase 1 split into 6–10 milestones. For each: scope, files, tests, benchmark test IDs satisfied, Section 8.13 lessons covered, recommended build model, and whether it needs an independent review session.
9. `docs/DEVELOPMENT_ROADMAP.md`: P0–P5 in detail (including the a/b sub-gates) with measurable acceptance criteria from Blueprint Part D; P6–P9 in outline.
10. `benchmarks/` skeleton per `RESEARCH_BENCHMARK.md` Section 9 (methodology, RS-1..RS-12 scenario YAML, runner stubs, report template, deterministic fixtures for P1 tests), and `docs/COMPETITIVE_BENCHMARK.md` derived from `RESEARCH_BENCHMARK.md` Section 5.3 (Hermes Agent primary; OpenClaw and OpenJarvis exploratory; versions `TBD_AT_EXECUTION`). A plan only: do not install or run any competitor.
11. `docs/PROPOSALS.md` (empty template) for frozen-scope ideas.
12. Repository skeleton (Go module, Python package, Makefile) so `make lint` and `make test` run and pass. Replace the Commands section of `AGENTS.md` with the real commands (the only change you may make to `AGENTS.md`).
13. `README.md`: project summary and how to run lint and tests.

## Constraints

- No production feature code in Phase 0.
- Design for the 2 vCPU / 16 GB RAM development VPS. No Redis, Kafka, PostgreSQL, Kubernetes, mandatory local LLM, or agent frameworks.
- Do not design beyond P1 except the contracts listed in task 3 and the roadmap outline.
- Do not implement or scaffold Control Plane, billing, subscriptions, VM provisioning, licensing, enterprise connectors, SSO, workspace membership, multi-tenant tables, browser runtime, OCA runtime, supervisor, learning loop, profile UI, or proactive scheduler.
- Every dependency follows Blueprint Section 16.11; list each with its license.
- `max_concurrent_tasks` is 1 in Phase 1 unless interleaving is explicitly designed and tested.
- Use the defaults in Blueprint Section 19 for open owner decisions; do not block on them.
- Record ambiguities in `ASSUMPTIONS.md`; list significant ones under "Questions for owner".

## Output

End with the Completion Report from `AGENTS.md` Section 10, plus:
- **Questions for owner** needed before Phase 1.
- **Top 5 risks** for Phase 1 with mitigations.
- **Proposed first Phase 1 milestone.**
