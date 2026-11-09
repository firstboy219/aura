# ARCHITECTURE.md — AURA Phase 1 architecture

Evidence level: **design-reviewed**. Source: Master Blueprint §5–§13, Part E; decisions in `docs/ADR/`.

## 1. Shape
Modular monolith: one Go Core binary (`aura`) + one on-demand Python worker process, inside one container
`aura-core` of the separate Compose project `aura` (ADR-0014). SQLite (WAL) on the `aura-data` volume.

```
 CLI (aura …) ──HTTP 127.0.0.1:7400──▶ ┌──────────────── GO CORE (Hands + Keys) ────────────────┐
                                        │ api/        REST /api/v1, bearer auth, rate limit, IDs │
                                        │ task/       state machine, event log, recovery         │
                                        │ policy/     grants, manifest, approvals (A0/A2/A3)     │
                                        │ budget/     reservations, reconciliation, kill switch  │
                                        │ gateway/    OpenRouter adapter, tiers, model registry  │
                                        │ tools/      web.fetch (SSRF), workspace.read/write     │
                                        │ store/      SQLite single-writer queue + read pool     │
                                        │ worker/     supervisor: spawn, idle stop, restart      │
                                        │ audit/, obs/ slog JSON, redaction, resources           │
                                        └──────────────▲─────────────┬────────────────────────────┘
                                                       │ JSON-RPC 2.0│ newline-delimited, stdio
                                        ┌──────────────┴─────────────▼────────────────────────────┐
                                        │ PYTHON WORKER (Brain, no keys): plan → step → complete │
                                        └──────────────────────────────────────────────────────────┘
```

## 2. Go packages (P1 target layout)
`core/go/cmd/aura` (CLI + `serve`), `internal/{app,config,store,task,policy,budget,gateway,tools,rpc,worker,engine,api,obs,
ids,contracts}`. Audit writes live in `policy.Audit`. Packages depend inward on `store` and `task`; `gateway` and `tools` never import `api`.

## 3. Task lifecycle (P1)
States: `CREATED → PLANNING → RUNNING ⇄ WAITING_APPROVAL → SUCCEEDED | FAILED | CANCELLED | BUDGET_EXCEEDED`, plus
`PAUSED` (owner) reachable from PLANNING/RUNNING/WAITING_APPROVAL. Every transition is an event appended in the same
transaction that updates the `tasks` projection. Startup recovery (ADR-0013 lease): non-terminal tasks resume from the
last checkpoint; a step with `STEP_STARTED` but no outcome is handled by its side-effect class (§8.1):
`pure`/`read` → rerun; `write_idempotent` → inspect recorded path+hash, then skip or retry with the same idempotency
key; `write_external` → not used by P1 tools, would go to `WAITING_APPROVAL` with `OUTCOME_UNKNOWN`.

## 4. Worker loop (P1 minimal, E.2 item 8)
1. Core → `task.plan` (objective, granted tools, budget). Worker → `llm.complete(tier=standard, response_schema)`.
2. For each planned step: Core → `task.step`; worker returns `invoke_tool | complete | fail`; Core validates and runs
   `tool.invoke` through policy → budget → tool → event.
3. Completion check is deterministic in Core (e.g. required workspace file exists with non-empty content, every
   planned step succeeded). Full verifier/replanning is P2.

## 5. Model Gateway
Logical tiers `fast | standard | deep` → model registry entry {display name, provider API model ID, OpenRouter
route ID, context limit, structured-output support, price/token, health}. Route IDs are filled and verified in P1
(MODEL_ASSIGNMENT.md is MISSING, A-02). Fallback only among configured OpenRouter routes. Structured output via
provider JSON-schema response format; invalid output → one repair attempt → step fails with `invalid_model_output`.

## 6. Dependencies (ADR-0015)
| Module | Purpose | License |
|---|---|---|
| modernc.org/sqlite v1.62.0 (+modernc libc/memory/mathutil) | SQLite, pure Go | BSD-3-Clause |
| gopkg.in/yaml.v3 | config | MIT / Apache-2.0 |
| github.com/oklog/ulid/v2 | IDs | Apache-2.0 |
| github.com/santhosh-tekuri/jsonschema/v6 | structured-output and contract validation (ADR-0015 amendment) | Apache-2.0 |
| Python: none at runtime; dev: pytest (MIT), ruff (MIT), jsonschema (MIT) | | |

## 7. Isolation from Jarvis
See ADR-0014 and ASSUMPTIONS A-05. Nothing in AURA reads from or writes to Jarvis code, containers, networks,
volumes, databases, Qdrant, n8n, or Hermes. Jarvis integration is P5 (adapter only, §15).
