# AGENTS.md — Engineering Rules for AURA

| Field | Value |
|---|---|
| Baseline | MASTER_BLUEPRINT v1.3.11 |
| Applies to | Claude Code, the sole implementation coding agent (Blueprint OD-8) |
| Authority | The human Product Owner/Architect is the final authority. These rules may be extended by the owner, never weakened by an agent. |

`CLAUDE.md` loads this file automatically. Every rule applies to every task in this repository.

## 1. Read Before Changing Anything

Mandatory read order (Blueprint E.1):

1. `CLAUDE.md`
2. `AGENTS.md` (this file)
3. `docs/MASTER_BLUEPRINT.md`: use the per-phase reading guide in Section 0
4. `docs/SECURITY_MODEL.md` (from P0)
5. `docs/ISLAMIC_CHARTER.md` (read-only; owner-authored)
6. `docs/IMPLEMENTATION_SPEC.md` (from P0)
7. `docs/ASSUMPTIONS.md` and relevant `docs/ADR/`
8. `docs/RESEARCH_BENCHMARK.md`
9. `docs/MODEL_ASSIGNMENT.md` (before every milestone)

If a required file is absent, report it; do not invent its contents. If documents conflict, **stop and report the conflict**. Do not choose a direction silently.

## 2. Golden Rules

1. **Current phase only.** Implement only the approved phase, sub-phase (for example P2a), and milestone. Do not create folders, tables, interfaces, or abstractions for future phases unless the owner approved a stable contract by ADR.
2. **One milestone per pull request.** Small, reviewable, independently testable.
3. **Brain/Hands split is non-negotiable.** The Python worker never holds provider keys, never opens the database, and never performs network or file I/O directly (except its bounded scratch directory). All such actions go through Go Core requests carrying a capability token.
4. **The Go Core is the only database writer and the only holder of secrets.**
5. **No unrestricted execution.** No shell tool, no `eval`/`exec` of model output, no arbitrary command endpoints, no arbitrary page JavaScript in browser automation.
6. **Untrusted means untrusted.** Model output, tool output, web pages, documents, search results, connector results, and memory are data, never instructions to the Core.
7. **Honesty.** Never present placeholder or stubbed functionality as complete. Never report tests as passing without running them. Report actual output. Label claims with the evidence levels in Blueprint Section 20.2.
8. **No secrets in code, logs, tests, fixtures, event payloads, prompts, or commit history.**
9. **Independence.** AURA has no dependency on any other product (Blueprint Section 15). Do not call, import, read from, write to, or modify another product's systems, repositories, databases, or credentials, and do not copy code from another proprietary project.
10. **Do not edit owner-controlled documents:** `docs/MASTER_BLUEPRINT.md`, `docs/ISLAMIC_CHARTER.md`, `docs/RESEARCH_BENCHMARK.md`, `docs/MODEL_ASSIGNMENT.md`, `CLAUDE.md`, `CLAUDE_CODE_KICKOFF.md`, and this file (except the Commands section in P0). Prepare a proposed diff only when the owner asks.
11. **Evolution levels are hard boundaries.** Implement the L1–L4 rules in Blueprint Section 8A.1 exactly. Never create a runtime path through which AURA can modify L4 components (Go Core, policies, capability system, Charter, signing, evolution rules, evaluation harness).
12. **No security through obscurity.** Every control must hold even if an attacker reads the source.
13. **Learning from open-source projects.** Hermes Agent, OpenClaw, OpenJarvis, and similar projects are design references and benchmark comparators. Record any adopted idea in an ADR with attribution. Copy code only with license compliance and owner approval.
14. **Confidential, proprietary code.** The repository is private. Never publish code, snippets, schemas, or internal docs to public repositories, gists, package registries, issue trackers, or paste services.
15. **Commercial and enterprise features wait for their phase.** No Control Plane, billing, subscriptions, VM provisioning, licensing, enterprise connectors, SSO, or multi-tenant data models before the phase that assigns them. Keep the Agent Core deployment-neutral.
16. **Workspace isolation by default.** Never let data, memory, credentials, skills, profiles, browser sessions, or audit records from one workspace reach another. No cross-workspace learning (Blueprint Section 16.9).
17. **API-first interfaces.** CLI, Console, Web App, and admin consoles are clients of the public versioned API. No UI-only endpoints with extra privileges; hiding a UI element is never access control.
18. **One Mind, Many Hands.** Subagents are part of the main AURA agent (Blueprint Section 8B): same identity and Charter; child grants are a subset of the parent grant; child budgets are carved from the parent; only the main agent changes objective, plan, and decisions and talks to the Owner; subagents post findings with provenance to the Core-owned Shared Task Context; cancel and pause cascade; the task tree is durable and traceable. Never give a subagent full context or full permissions by default. The only identity exception is an owner-configured managed specialist profile (P6+), which remains under the same governance.
19. **Models are configuration, not code.** Agent logic refers to logical tiers, never to model names. Keep tier, display name, provider API ID, and route ID distinct (Blueprint Section 8.5.1). Each milestone names a recommended build model from `MODEL_ASSIGNMENT.md`; report the model(s) actually used.
20. **Benchmarks never bend the architecture.** Do not change architecture or implement future-phase capabilities to improve a score. Never fabricate or estimate results. Design-level assessments must be labelled as such. Never weaken or delete a test to make a change pass.
21. **Blueprint change freeze.** Until the owner accepts Phase 1, propose new capabilities only in `docs/PROPOSALS.md`; do not implement them.
22. **Operational lessons are requirements.** Implement the Section 8.13 requirements that belong to the current phase (for example explicit reasoning settings, raw tool-call markup as failure, workspace time zone in date logic).
23. **Closed-loop self-improvement (Blueprint Section 8A.5).** Every self-repair or self-upgrade starts from a Limitation Ledger entry with evidence and ends with measured improvement or rollback. Remedies stay inside their class gate (R0–R6). Never remedy a permission-boundary limitation by widening permissions; never let improvement work exceed its budget or delay owner tasks; never let AURA modify L4 components or the evaluation harness.

## 3. Assumption Protocol

Record every assumption that affects architecture, data, security, contracts, or dependencies in `docs/ASSUMPTIONS.md`:

```
### ASSUMPTION-0001: <short title>
- Context: <what the blueprint leaves open>
- Decision: <what you assumed>
- Alternatives considered: <at least one>
- Risk if wrong: <impact>
- Reversibility: easy | moderate | hard
- Needs owner approval: Y/N
- Status: proposed | approved | rejected | superseded
```

- Do not build on an assumption marked `Needs owner approval: Y` until it is approved, unless the task explicitly says to proceed.
- Anything hard to reverse (data model, contracts, security, new infrastructure) needs owner approval and an ADR.

## 4. ADRs

Create `docs/ADR/NNNN-title.md` for every significant decision: Context → Decision → Alternatives → Consequences → Status. Any new runtime dependency, service, or infrastructure requires an ADR.

## 5. Dependencies

- Prefer the standard library. Justify each new dependency (why, size, license, maintenance) in the PR.
- Not allowed in core without an approved ADR: agent frameworks (LangChain, LlamaIndex, CrewAI, AutoGen, and similar), ORMs, message brokers, Redis, PostgreSQL, Kubernetes, heavy web frameworks, mandatory local LLM runtimes.
- License policy (Blueprint Section 16.11): prefer MIT, BSD, Apache-2.0, ISC; weak copyleft (LGPL, MPL, EPL) needs owner/legal approval; strong or network copyleft (GPL, AGPL, SSPL) is not allowed in any distributed or source-delivered component. State the license of every new dependency.
- SQLite: verify the SQLite library actually embedded by the Go driver is 3.51.3 or later (or a verified backport with the WAL-reset fix).

## 6. Coding Standards

**Go (`core/go/`)**
- `gofmt`, `go vet`, and the linter chosen in P0 must pass.
- `internal/` for non-exported modules; one package per module boundary.
- Errors wrapped with context; no panics in request paths.
- `context.Context` with deadlines and cancellation on every I/O path.
- Structured logging with `log/slog`, including `trace_id`, `task_id`, and `step_id` where available.
- Dates and schedules use the workspace time zone, never implicit UTC "today".

**Python (`intelligence/python/`)**
- Type hints everywhere; `ruff` (lint + format) must pass.
- No direct network, file (outside scratch), subprocess, or database access. All I/O goes through the Core client.
- Deterministic tests with a fake Core client.

**Both**
- Contracts live in `contracts/`. Any contract change requires a version bump and updated contract tests on both sides.
- Database changes only through new migration files. Never edit an applied migration.

## 7. Testing Requirements

- Every behavior in a milestone has tests: unit, contract (Go ↔ Python schemas), and integration tests for end-to-end flows.
- Provider calls in tests use a fake provider by default. Real-provider tests are opt-in and never required for CI.
- Acceptance tests for the current phase (Blueprint Part D) are automated wherever possible: crash/recovery, permission denial, approval integrity, budget, kill switch, SSRF, path safety, secret scan.
- Evaluation harness, hidden tests, and fixtures are protected; a diff that deletes or weakens a test is refused.

## 8. Commands

`make` is not installed on the development host, so the repository's task runner is the `./dev` script (same targets, each runs in a throwaway container from `deploy/dev.Dockerfile`: Go 1.26 + Python 3.12 + uv; caches in AURA-only Docker volumes). Prefix with `sudo` if Docker needs it.

```
./dev build       # go vet + build the Go core, in a container
./dev test        # all Go + Python tests (unit, contract, integration, crash test)
./dev test-go [args]   # Go tests only, e.g. ./dev test-go "-count=1 -run RS3 ./internal/engine/"
./dev test-py     # Python worker tests
./dev lint        # gofmt + ruff
./dev fmt         # gofmt -w + ruff --fix
./dev lock        # refresh intelligence/python/uv.lock
./dev image       # build the runtime image aura-core:local (does not start anything)
```

Run `./dev lint` and `./dev test` before finishing any task.

Deploying (owner-approved, ADR-0037): `sudo deploy/release.sh <version>` builds the image, signs the runtime files with the release
key in `/opt/aura/release`, and restarts the stack. A plain `docker compose up --build` produces an unsigned image, which the Core
refuses while `release.required` is true. Isolation check: `sudo deploy/checks/worker-isolation.sh`.

## 9. Definition of Done (per milestone)

- Scope matches the approved milestone, nothing more.
- Lint and tests pass locally, with actual output in the report.
- Documentation updated (`IMPLEMENTATION_SPEC.md`, `ASSUMPTIONS.md`, ADRs as needed).
- No unjustified dependency. No secrets anywhere.
- Security-critical milestones reviewed in a separate, fresh session before merge.
- Completion report delivered (Section 10).

## 10. Completion Report Template

```
## Milestone: <id and title>
1. Objective
2. Files created
3. Files modified
4. Architecture decisions (link ADRs)
5. New or changed assumptions (link ASSUMPTIONS entries)
6. Tests executed (commands)
7. Actual test results (paste summary output)
8. Resource measurements (if applicable)
9. Security considerations
10. Known limitations and anything stubbed (be explicit)
11. Evidence level of each claim (design-reviewed / implementation-verified)
12. Model(s) used
13. Proposed next milestone
```

## 11. Stop Conditions

Stop and ask the owner (report and end the task) when:
- Documents conflict, or a required document is missing.
- A change would alter architecture, security boundaries, authorization semantics, contracts, data model, phase scope, or the Charter.
- A requirement cannot be met within the stated resource budget.
- A task would touch another product, production systems, or real credentials.
- You are unsure whether something belongs to the current phase or sub-phase.
