# ADR approval candidates (generated 2026-11-17, evidence only)

No ADR file was changed: approving an ADR is the owner's decision. This list shows which **Proposed** ADRs are already referenced by running code, migrations, tests or deploy files (so approving them records reality), and which have no code reference (decision-only or unimplemented; review before approving).

Proposed ADRs: 65; referenced in code: 57; not referenced: 8.

## Referenced in code (candidates to accept as implemented)

| ADR | Title | code refs | example |
|---|---|---|---|
| 0001 | SQLite driver, embedded version, WAL durability, checkpoint and backup | 2 | `core/go/internal/store/store.go` |
| 0002 | Configuration format and secrets loading | 2 | `core/go/internal/config/config.go` |
| 0003 | Go–Python transport | 5 | `core/go/internal/worker/supervisor.go` |
| 0005 | Identifier format | 1 | `core/go/internal/ids/ids.go` |
| 0006 | Capability grant model | 1 | `core/go/internal/policy/policy.go` |
| 0007 | Worker trust and scratch boundary (P1) | 1 | `core/go/internal/worker/supervisor.go` |
| 0008 | Event retention and audit/content separation | 4 | `core/go/internal/obs/redact.go` |
| 0009 | Benchmark execution environment | 1 | `core/go/internal/gateway/gatewaytest/fake.go` |
| 0010 | Browser egress boundary (contract only; implementation P4) | 1 | `core/go/internal/tools/ssrf.go` |
| 0012 | Commercial boundary (contract only) | 2 | `core/go/internal/store/store.go` |
| 0013 | Stable contracts for P2 supervisor, activity events and telemetry | 3 | `core/go/internal/engine/engine.go` |
| 0016 | P2 agent loop: replanning, verification, plan review, clarification, cascade | 2 | `core/go/internal/engine/loop.go` |
| 0017 | Provenance-bound dynamic tool arguments | 4 | `core/go/internal/engine/step.go` |
| 0018 | Persistent Agent Supervisor v1 | 2 | `core/go/internal/engine/supervisor.go` |
| 0019 | Subagents v1 (sequential) and Shared Task Context | 3 | `core/go/internal/engine/subagents.go` |
| 0020 | MCP client adapter | 3 | `core/go/internal/config/config.go` |
| 0021 | Second model provider and web search providers | 4 | `core/go/internal/config/config.go` |
| 0022 | Event stream and Console TUI | 3 | `core/go/internal/console/console.go` |
| 0023 | Memory with provenance (P3) | 1 | `core/go/internal/engine/memory.go` |
| 0024 | Identity and Charter framework (P3) | 4 | `core/go/internal/config/config.go` |
| 0025 | Learning L1: lessons and skills (P3) | 2 | `core/go/internal/engine/loop.go` |
| 0026 | Owner Working Profile (P3) | 1 | `core/go/internal/engine/profile.go` |
| 0027 | Self-Model v1, efficiency scorecard, shadow routing (P3) | 2 | `core/go/internal/engine/selfmodel.go` |
| 0028 | Scheduled triggers and Proactive Intelligence v1 (P3) | 1 | `core/go/internal/engine/triggers.go` |
| 0029 | Task state names and the representation of INTERRUPTED / RECOVERING | 1 | `core/go/internal/store/migrations/0003_p3_memory_learning.sql` |
| 0032 | Clarification timeout policy (`INPUT_TIMEOUT`) | 3 | `core/go/internal/engine/inputs.go` |
| 0034 | Authenticated inbound webhooks | 5 | `core/go/internal/config/config.go` |
| 0035 | Worker OS isolation: a separate, network-less container | 4 | `core/go/cmd/aura/main.go` |
| 0036 | Encrypted secrets at rest, key rotation, backup/restore | 8 | `core/go/cmd/aura/main.go` |
| 0037 | Signed runtime artifacts | 5 | `core/go/cmd/aura/main.go` |
| 0038 | L2 tools in a WASM sandbox (remedy R3) | 11 | `core/go/cmd/aura/main.go` |
| 0039 | Browser Automation v1 | 10 | `core/go/internal/egress/egress.go` |
| 0040 | Browser login, one-time codes, downloads and uploads | 10 | `core/go/cmd/aura/main.go` |
| 0041 | Workspace Web App, owner authentication and remote exposure | 10 | `core/go/cmd/aura/main.go` |
| 0042 | Workspace handoff and owner sign-in through the remote browser view | 3 | `core/go/internal/engine/browser_handoff.go` |
| 0043 | Workspace Admin foundation: members, roles, budget, write-only credentials | 7 | `core/go/cmd/aura/main.go` |
| 0044 | First messaging channel: Telegram bot | 5 | `core/go/internal/channel/telegram.go` |
| 0045 | First connector pilot: GitHub, read-only | 4 | `core/go/internal/config/config.go` |
| 0046 | Specialist subagent profiles | 3 | `core/go/internal/config/config.go` |
| 0047 | Worker pool and parallel subagents | 8 | `core/go/internal/config/config.go` |
| 0048 | L3 brain variants through AURA Lab (R4 remedies) | 7 | `core/go/cmd/aura/main.go` |
| 0049 | Enterprise Admin: departments, audit search/export, usage by department | 3 | `core/go/internal/api/admin_p6.go` |
| 0050 | AUL v1: capability-safe workflows compiled to IR and run in a WebAssembly VM | 15 | `core/go/cmd/aura/main.go` |
| 0051 | P9 supply-chain checks: reproducible build and license audit | 2 | `deploy/checks/reproducible-build.sh` |
| 0052 | Android client v0.1 (P7) | 2 | `deploy/android-build.sh` |
| 0053 | App accounts: username/password sign-in for the Android client | 5 | `core/go/internal/api/app_accounts.go` |
| 0054 | Adaptive routing: route health, immediate rest on quota, tier rescue | 6 | `core/go/internal/config/config.go` |
| 0055 | Owner reply and adaptive view | 15 | `core/go/internal/channel/telegram.go` |
| 0070 | ODI: NEXUS gateway, AURA-owned fallback and Teacher Gate (design only) | 1 | `core/go/internal/nexus/teacher.go` |
| 0071 | ODI A2: derived canonical state, verification levels and truthful claims | 2 | `core/go/internal/engine/reply.go` |
| 0072 | ODI A3: approval accountability and revocation, tamper-evident audit, risk label | 5 | `core/go/internal/engine/odi_a3_test.go` |
| 0073 | ODI A4: NEXUS gateway, AURA-owned fallback, Teacher Gate | 6 | `core/go/internal/nexus/router.go` |
| 0074 | ODI A5: tool manifests, per-tool quotas, isolation evidence | 3 | `core/go/internal/engine/odi_a5_test.go` |
| 0075 | ODI A6: device registry, pairing, signed requests, revocation, takeover leases | 4 | `core/go/internal/device/device.go` |
| 0076 | ODI A7: trigger policy, memory expiry, notification hygiene, retention report | 3 | `core/go/internal/engine/ops_odi.go` |
| 0077 | ODI A8: self-engineering change levels, approval rules and evidence-gated rollou | 4 | `core/go/internal/api/changes.go` |
| 0078 | ODI: NEXUS learns from the external answers AURA obtained for heavy public tasks | 3 | `core/go/internal/nexus/teach.go` |

## No reference found in code (review first)

| ADR | Title |
|---|---|
| 0004 | Python tooling |
| 0011 | Owner Communication Agent contracts (contract only; implementation P5) |
| 0014 | Deployment as an independent Docker stack |
| 0015 | HTTP server and Go dependency policy |
| 0030 | Limitation Ledger v1 and the closed self-improvement loop (Blueprint §8A.5) |
| 0031 | R0 runtime self-healing and workspace time zone (OL-01/02/03/08) |
| 0033 | Per-task recovery policy (`auto` / `manual` / `never`) |
| 0069 | ODI A1: canonical lifecycle mapping, error/event contracts, acceptance catalogue |

Method: a regular expression for `ADR-00NN` / `ADR 00NN` over Go, SQL, Python and deploy files. A reference proves the code points at the decision, not that it matches every sentence of it.
To accept one: edit its status line to `Accepted by the owner <date>` (per ADR) — or tell me which numbers to mark.
