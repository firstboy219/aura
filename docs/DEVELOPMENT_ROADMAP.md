# DEVELOPMENT_ROADMAP.md

**Current phase: P2 — Agent Loop + Subagents v1 + Persistent Supervisor + Console v1** (P1 approved 2026-11-09; owner
decided P2 then P3 without a pause, ASSUMPTIONS A-21). P1 evidence: `benchmarks/reports/P1-2026-11-09.md`.
Order is binding (Blueprint Part D); each phase ends with a completion report and explicit owner approval.

## Detailed (P0–P5)
| Phase | Outcome | Gate |
|---|---|---|
| P0 | Docs, ADR-0001…0015, contracts (OpenAPI + JSON-RPC schemas), benchmark skeleton, buildable Go/Python skeleton, separate Docker stack definition | `./dev build`, `./dev test`, `./dev lint` pass; owner approves |
| P1 | Go Core runtime + vertical slice M1–M9; RS-1 | D.1 P1 criteria with evidence pack; owner approves |
| P2 | Planning/verify/replan, cascade, MCP client, `web.search`/`data.read`/`calc`, `WAITING_INPUT`, Persistent Supervisor v1, activity timeline, subagents v1 (sequential), SSE stream, Console TUI v1; second provider fallback; RS-2, RS-7, RS-10 | D.1 P2 |
| P3 | Memory (FTS5 + optional embeddings), identity, Charter enforcement (needs owner Charter), learning L1 skills, self-model v1, Owner Working Profile, shadow adaptive routing, read-only proactive research, Console v2; RS-3, RS-11, RS-12 — **Agent MVP** | D.1 P3 incl. GOV-01…06 |
| P4 | OS isolation of worker, sandbox, L2 tool evolution, encrypted secrets, signed artifacts, Playwright browser v1; RS-4, RS-6, RS-8 | D.1 P4 |
| P5a | OCA live voice, Workspace Web App, cross-channel continuity, activity view, handoff; RS-5, RS-9 | P5a gate |
| P5b | One messaging channel, first connector pilot (no integration with any other product; Blueprint §15) | P5b gate |

## Outline (P6–P9)
P6 self-model v2, L3 evolution, AURA Lab, specialists, Enterprise Admin Console · P7 Android + device perception ·
P8 AUL → WASM · P9 commercial release, VM provisioning, Control Plane, licensing.
