# ASSUMPTIONS.md

Each entry: assumption, alternatives, rationale, risk. Status: `open` (default in use, owner may change) or
`confirmed` (owner decided). Evidence level of this whole document: **design-reviewed**.

## Owner decisions recorded on 2026-11-09

| ID | Assumption / decision | Alternatives | Rationale | Risk | Status |
|---|---|---|---|---|---|
| A-01 | This round implements **P0, stops for owner review, then P1**. | P0 only; P0+P1 without a gate | Owner choice; matches Part D "explicit owner approval before the next phase" | Owner review delays P1 | confirmed |
| A-02 | The owner's companion documents were supplied on 2026-11-09 (v1.3.11 package) and installed verbatim; the earlier MISSING placeholders were removed. `ISLAMIC_CHARTER.md` is an unapproved v0.1 draft (rules/sources `[OWNER]`), so Charter enforcement and GOV-01..05 remain pending. | — | Owner supplied them | Charter content still absent | **resolved (Charter draft only)** |
| A-03 | Repository: new **private GitHub repo** (name proposed `firstboy219/aura`); local clone `~/aura`. Until the owner creates it, commits stay local. | Local-only git | Owner choice; Blueprint §15 "separate products and repositories" | None until remote exists | confirmed (remote pending) |
| A-04 | P1 used a **copy of the OpenRouter key that belongs to another product**, placed in `/opt/aura/secrets/openrouter_key` at the owner's explicit request. **Conflict (checkpoint v1.3.11):** Blueprint §15 and AGENTS rule 9 forbid reading another product's credentials; the Core never reads that product's files, but the key was copied from them. **Recommendation:** the owner creates a dedicated AURA key (own credit limit) and replaces the file; the old copy should be considered shared. Until the owner decides, no further reads are made. | Dedicated AURA key | Owner decision at the time, before the independence rule (v1.3.10) was in force | Mixed spend and mixed exposure with the other product | **decided 2026-11-10 by the owner: keep the shared key** ("tetap pakai key openrouternya jarvis karena memang untuk sharing"). An intentional, owner-approved exception to §15 for the provider credential only: AURA still never reads the other product's files, code or data; the key lives in AURA's own vault (`vault:openrouter`). Spend stays shared (A-08). |
| A-05 | AURA runs as a **separate Docker Compose project** `aura` (container `aura-core`, network `aura-net`, volume `aura-data`, host port `127.0.0.1:7400`), never on another product's networks/volumes/proxy. | Native process on host; a network shared with other products | Owner request "docker yang terpisah"; §12 co-resident isolation | A Docker container is a deployment boundary, **not** the P4 worker sandbox; see SECURITY_MODEL §4 | confirmed |

## Engineering assumptions (defaults; owner may override)

| ID | Assumption | Alternatives | Rationale | Risk |
|---|---|---|---|---|
| A-06 | Go 1.26 toolchain in Docker (`golang:1.26-bookworm`); Go is not installed on the host. | Install Go on host | Keeps host untouched; `modernc.org/sqlite` v1.62.0 requires Go ≥ 1.26 | Image pull size (~900 MB, cached) |
| A-07 | `make` is not installed on the host; the task runner is `./dev` (Blueprint allows "Makefile or equivalent"). | Install make | Avoid host changes | None |
| A-08 | Budget defaults from Blueprint §13: $0.50/task, 20 iterations, 900 s wall, $5/day global. | Owner-specific limits | Explicit Blueprint default | The key in use is shared with another product (A-04), so their spend and AURA's share OpenRouter credit |
| A-09 | Container limits `cpus: 1.0`, `mem_limit: 1g`, `pids_limit: 256` for `aura-core` (Core + worker). | No limits | Host is shared with ~15 containers of the owner's other products (measured below) | Limits may need tuning after P1 baseline |
| A-10 | Single instance = single workspace boundary in P1 (Blueprint §10); no workspace/tenant tables. | Multi-workspace schema | Blueprint scope rule | None in P1 |
| A-11 | Hermes is **not** installed or run (E.1). Competitive benchmark is plan-only in P0. | — | Blueprint E.1 | None |
| A-12 | Development target is this shared host, not the 2 vCPU/16 GB reference VPS (see facts). Measurements are labeled with the actual host. | Dedicated VPS | Owner asked to run alongside other products | Shared-host noise in measurements (§12.1 item 3) |

## Owner decisions recorded later

| ID | Decision | Date | Note |
|---|---|---|---|
| A-21 | **P1 approved.** The owner then chose "P2 then P3 without a pause" when asked (the Blueprint requires owner approval between phases, Part D). | 2026-11-09 | The P2→P3 gate is skipped by explicit owner decision. P2 still ends with its own evidence pack; P3 builds on it immediately. Phase order itself is kept (P3 is not started before P2 code exists). |
| A-22 | **web.search defaults to Wikipedia** (official keyless API, English edition); Brave Search is available with an owner key file. Search results are fetchable only if their domain is in the task's `allow_domains` (e.g. `*.wikipedia.org`) — the allowlist stays the owner's decision. | 2026-11-09 | No free general web-search API without a key exists that is not scraping; scraping search engines is avoided. |
| A-23 | Files named in the objective that the task only **reads** (attachments, `workspace.read`/`data.read` paths not written) are inputs, not required outputs. Without completion criteria, model verification is skipped and recorded as `VERIFICATION_SKIPPED` (deterministic checks still run). | 2026-11-09 | Prevents both false failures (inputs demanded as outputs) and vacuous "verification". |
| A-24 | `BUDGET_EXCEEDED` stays a terminal outcome next to `COMPLETED/FAILED/CANCELLED` (Blueprint §8.4 says "end with BUDGET_EXCEEDED"; §8.2 lists it as a reason code). | Fold into `FAILED` + reason code | Both readings exist in the Blueprint | Tooling expecting only three terminal states | open (ADR-0029) |
| A-25 | Limitation Ledger task class (v1) = sorted tool set; "verified success" counts a completed task as success if it was verified, or if verification never applied to it (pre-P2 tasks). | Owner-visible class labels (PROPOSAL-0002) | Deterministic and cheap | Different jobs with equal tools share a class | open |
| A-26 | The wall-clock budget of a task starts when it starts running, not when it was queued. | Count queue time | Found by the ledger: queued tasks were ending as `MAX_WALL_TIME` before running | A task can wait arbitrarily long in a queue (one task runs at a time) | decided |
| A-27 | Workspace time zone defaults to `Asia/Jakarta` (OL-08). | UTC | Owner's zone (WIB) in the OL-08 test requirement | Owners elsewhere must set `timezone` | decided |


## Phase 1 implementation assumptions (2026-11-09)

| ID | Assumption | Alternatives | Rationale | Risk |
|---|---|---|---|---|
| A-13 | **Model profile — confirmed by MODEL_ASSIGNMENT.md v1.2** (same tier→model mapping: Haiku 5.5 / Sonnet 5.5 / Opus 5.5, reasoning low/medium/high; free routes are an owner-approved addition, A-20; Fable 5.1 candidate not evaluated): fast = `anthropic/claude-haiku-5.5` (fallback `claude-haiku-4.5`), standard = `anthropic/claude-sonnet-5.5` (fallback `claude-sonnet-5`), deep = `anthropic/claude-opus-5.5` (fallback `claude-opus-5`). Route IDs, structured-output support and prices verified against the public OpenRouter catalog on 2026-11-09 (`deploy/config.yaml`). | Owner-specified profile | Blueprint §6 names the Claude family as default; fallbacks stay inside OpenRouter (§8.5) | Replace when the owner supplies MODEL_ASSIGNMENT.md; `aura doctor` re-checks routes and prices |
| A-14 | A step attempt interrupted by a **Core crash** is not counted against `max_attempts` (3); instead each step may be resumed after at most 10 Core restarts (`recovery_limit`). | Count crashes as attempts | Found by the crash test: three crashes during one write step failed an otherwise healthy task | A Core crash loop on one step ends after 10 recoveries |
| A-15 | The global daily budget window is the **UTC** day. | WIB day | Simple and unambiguous | Window resets at 07:00 WIB |
| A-16 | `web.fetch` returns text extracted from HTML by a small built-in extractor (scripts/styles removed). Hidden text is kept and treated as untrusted like all content. | HTML parser library | Stdlib-first (§6); content is untrusted either way | Some pages extract poorly |
| A-17 | Each task has its own workspace directory `<workspace.root>/<task_id>/`; `workspace.*` paths are relative to it. | One shared workspace | Isolation between tasks; A0 "writes in task workspace" (§8.3) | Tasks cannot read each other's files (by design in P1) |
| A-18 | A tool call must use the **arguments fixed in the plan** (URL for `web.fetch`, path for `workspace.*`); only `workspace.write` content is produced at step time. | Let the worker choose arguments per step | Content read during the task cannot redirect a tool (P1 injection baseline) | Plans must name URLs and paths up front |
| A-19 | `write_external` recovery (`OUTCOME_UNKNOWN` → approval) is designed but has no P1 tool to exercise it. | — | No P1 tool has that class | Must be tested when the first external-write tool arrives |
| A-20 | **Free model routes** (owner, 2026-11-09: "pakai AI yang free dulu", OpenRouter credit is 0): `free: true` routes with price 0 are allowed (a $0 reservation still counts against `max_iterations`); every free route must declare `prompt_logging: may_log`, and such routes serve **only tasks marked `data_class: public`** — private tasks (the default) skip them, so personal content never reaches an endpoint that may log prompts. Models without native JSON-schema output use `structured_output: prompt` (schema in the prompt, same Core validation and single repair). | Paid routes only | Owner request; keeps the owner's rule that personal content never goes to free endpoints | Free models are weaker and rate-limited (Retry-After honoured, capped 20 s); results are labelled with the route used |

## Host facts recorded 2026-11-09 (P0 must record facts before proposing structure)

- Host: AWS, Intel Xeon Platinum 8259CL, **4 vCPU**, 15.4 GiB RAM, ext4 root 309 GB (75 GB free), kernel
  7.0.0-1014-aws, Docker 29.9.1. Python 3.12.3 on host; no Go, no make, no uv on host.
- Co-resident containers of the owner's other products (docker stats, idle moment): total ≈ 6.4 GiB RSS across 15
  containers (largest 1.5 GiB), each < 2 % CPU. CPU each < 2 %; load average 3.75/1.93/1.00 (sample taken
  while AURA images were being built). PM2-managed autotoko backend runs natively.
- Host ports already in use: 22, 80, 443, 81 (loopback), 5433 (loopback), 6333 (loopback), 8090, 8091.
  **7400 is free.**
- Docker networks present: `bridge`, `host`, `none`, other products' networks. AURA creates
  `aura-net` only.

## Companion-document reconciliation (E.1 gate) — 2026-11-09, v1.3.11 package

| Document | Version | sha256 (first 16) | Status |
|---|---|---|---|
| docs/MASTER_BLUEPRINT.md | 1.3.11 | f2b59c9e8916283a | installed verbatim (replaces v1.3.9 `ea7d4a25…`) |
| AGENTS.md | for v1.3.11 | 59a86eaddac7e7a6 | installed verbatim; only the Commands section edited (allowed) |
| CLAUDE.md | for v1.3.10+ | 1dcf26b1d4a4f88e | installed verbatim |
| CLAUDE_CODE_KICKOFF.md | for v1.3.11 | 98704a163d2812d4 | installed verbatim |
| docs/RESEARCH_BENCHMARK.md | 1.3 | cf87aaf5369e7fa0 | installed verbatim |
| docs/MODEL_ASSIGNMENT.md | 1.2 | 2bd1f81ed60f04c2 | installed verbatim |
| docs/ISLAMIC_CHARTER.md | 0.1 owner draft | e045e2357b5ec4a4 | installed verbatim; not approved for enforcement |
| SECURITY_MODEL.md, DATA_MODEL.md, API_SPEC.md, ARCHITECTURE.md, IMPLEMENTATION_SPEC.md | written by Claude Code | — | for owner review; `docs/CONFORMANCE_v1.3.11.md` lists where they trail the code |

### ASSUMPTION-0028: P5a acceptance gate waived for P5b
- Context: Blueprint D.1 P5 says not to start messaging/integration work until P5a (OCA + Web) passes owner acceptance.
- Decision: the owner instructed on 2026-11-10 to "continue to the last phase"; P5b (one channel, one connector pilot) is built
  while voice (OCA) is deferred by the owner and the Web App is not yet reachable publicly (DNS records pending in Cloudflare).
- Alternatives considered: stop at P5a and wait for acceptance.
- Risk if wrong: P5b work may need changes after P5a review.
- Reversibility: easy (channels and connectors are off unless configured).
- Needs owner approval: N (instructed)
- Status: approved

### ASSUMPTION-0029: P10 "Living Agent" work proceeds on the owner's instruction
- Context: the blueprint has no P10. On 2026-11-10 the owner asked to review and perfect AURA: obedient, answers the
  owner, adapts its interface to the task, manages its own needs, validated in a sandbox.
- Decision: implement adaptive routing (ADR-0054) and the owner reply with an adaptive view (ADR-0055) as milestones
  P10-M1/M2. "Self-awareness" is implemented as an operational self brief from real state only; no consciousness claim.
  Superiority over Hermes, OpenClaw or others is not claimed without a measured comparison (AGENTS rule 20).
- Alternatives considered: record in `docs/PROPOSALS.md` only and wait.
- Risk if wrong: the reply contract and `task_replies` table may need changes after owner review.
- Reversibility: moderate (`reply.disabled: true` turns the feature off; the table stays).
- Needs owner approval: Y (contract and data model change; instructed, review pending)
- Status: approved (owner, 2026-11-11: "setuju assumption0029")
