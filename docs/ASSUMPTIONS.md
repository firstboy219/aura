# ASSUMPTIONS.md

Each entry: assumption, alternatives, rationale, risk. Status: `open` (default in use, owner may change) or
`confirmed` (owner decided). Evidence level of this whole document: **design-reviewed**.

## Owner decisions recorded on 2026-11-09

| ID | Assumption / decision | Alternatives | Rationale | Risk | Status |
|---|---|---|---|---|---|
| A-01 | This round implements **P0, stops for owner review, then P1**. | P0 only; P0+P1 without a gate | Owner choice; matches Part D "explicit owner approval before the next phase" | Owner review delays P1 | confirmed |
| A-02 | `ISLAMIC_CHARTER.md`, `RESEARCH_BENCHMARK.md`, `MODEL_ASSIGNMENT.md`, `CLAUDE_CODE_KICKOFF.md` were not supplied; they are **MISSING placeholders**. P0/P1 use Blueprint defaults. | Owner sends them now; Claude drafts proposals | Blueprint E.1 forbids inventing them | P1 model routes and benchmark weights are unverified defaults; Charter rules cannot be evaluated (P3 anyway) | confirmed |
| A-03 | Repository: new **private GitHub repo** (name proposed `firstboy219/aura`); local clone `~/aura`. Until the owner creates it, commits stay local. | Local-only git | Owner choice; Blueprint §15 "separate products and repositories" | None until remote exists | confirmed (remote pending) |
| A-04 | P1 uses the **same OpenRouter key as Jarvis**. The owner copies it into `/opt/aura/secrets/openrouter_key` (0400) with an owner-run command; Claude Code never reads Jarvis `.env`. | Dedicated AURA key | Owner choice | Spend is mixed with Jarvis on the OpenRouter dashboard; AURA's own budget governor (A-08) bounds AURA spend, and usage records separate AURA's share | confirmed |
| A-05 | AURA runs as a **separate Docker Compose project** `aura` (container `aura-core`, network `aura-net`, volume `aura-data`, host port `127.0.0.1:7400`), never on Jarvis networks/volumes/proxy. | Native process on host; shared Jarvis network | Owner request "docker yang terpisah"; §12 co-resident isolation | A Docker container is a deployment boundary, **not** the P4 worker sandbox; see SECURITY_MODEL §4 | confirmed |

## Engineering assumptions (defaults; owner may override)

| ID | Assumption | Alternatives | Rationale | Risk |
|---|---|---|---|---|
| A-06 | Go 1.26 toolchain in Docker (`golang:1.26-bookworm`); Go is not installed on the host. | Install Go on host | Keeps host untouched; `modernc.org/sqlite` v1.62.0 requires Go ≥ 1.26 | Image pull size (~900 MB, cached) |
| A-07 | `make` is not installed on the host; the task runner is `./dev` (Blueprint allows "Makefile or equivalent"). | Install make | Avoid host changes | None |
| A-08 | Budget defaults from Blueprint §13: $0.50/task, 20 iterations, 900 s wall, $5/day global. | Owner-specific limits | Explicit Blueprint default | Shared key means a Jarvis spike and AURA spend share OpenRouter credit |
| A-09 | Container limits `cpus: 1.0`, `mem_limit: 1g`, `pids_limit: 256` for `aura-core` (Core + worker). | No limits | Host is shared with ~15 Jarvis/autotoko containers (measured below) | Limits may need tuning after P1 baseline |
| A-10 | Single instance = single workspace boundary in P1 (Blueprint §10); no workspace/tenant tables. | Multi-workspace schema | Blueprint scope rule | None in P1 |
| A-11 | Hermes is **not** installed or run (E.1). Competitive benchmark is plan-only in P0. | — | Blueprint E.1 | None |
| A-12 | Development target is this shared host, not the 2 vCPU/16 GB reference VPS (see facts). Measurements are labeled with the actual host. | Dedicated VPS | Owner asked to run alongside Jarvis | Shared-host noise in measurements (§12.1 item 3) |

## Host facts recorded 2026-11-09 (P0 must record facts before proposing structure)

- Host: AWS, Intel Xeon Platinum 8259CL, **4 vCPU**, 15.4 GiB RAM, ext4 root 309 GB (75 GB free), kernel
  7.0.0-1014-aws, Docker 29.9.1. Python 3.12.3 on host; no Go, no make, no uv on host.
- Co-resident containers (docker stats, idle moment): jarvis-brain 1.48 GiB, jarvis-core 1.01 GiB, jarvis-browser
  1.0 GiB, jarvis-ears 648 MiB, jarvis-tools 646 MiB, jarvis-executor 318 MiB, autotoko-postgres 313 MiB, jarvis-db
  185 MiB, jarvis-proxy 114 MiB, others < 40 MiB each. CPU each < 2 %; load average 3.75/1.93/1.00 (sample taken
  while AURA images were being built). PM2-managed autotoko backend runs natively.
- Host ports already in use: 22, 80, 443, 81 (loopback), 5433 (loopback), 6333 (loopback), 8090, 8091.
  **7400 is free.**
- Docker networks present: `bridge`, `host`, `none`, `jarvis_jarvis-network`, two `jarvis-test-*`. AURA creates
  `aura-net` only.

## Companion-document reconciliation (E.1 gate)

| Document | Version received | sha256 | Status |
|---|---|---|---|
| MASTER_BLUEPRINT.md | 1.3.9 (9 Nov 2026) | ea7d4a252238b8078beb7fb7303500bbfe8e02b98d2301360a4ff6093b4653a0 | present, identical in .md upload and package zip |
| CHANGE_SUMMARY v1.3.9 | in package zip | — | read; consistent with Blueprint header |
| ISLAMIC_CHARTER.md | — | — | MISSING |
| RESEARCH_BENCHMARK.md | — (Blueprint mentions v1.1, possibly later) | — | MISSING; version must not be assumed |
| MODEL_ASSIGNMENT.md | — | — | MISSING |
| CLAUDE_CODE_KICKOFF.md | — | — | MISSING |
| SECURITY_MODEL.md, IMPLEMENTATION_SPEC.md, AGENTS.md, CLAUDE.md | — | — | not supplied; **written in this P0** from the Blueprint for owner review |
