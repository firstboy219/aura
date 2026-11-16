# Status ADR AURA (dihasilkan 2026-11-16)

Total 61 nomor ADR unik (68 berkas; sebagian nomor ganda): accepted 11, proposed 49, lainnya 1. Banyak ADR "Proposed" sudah diimplementasikan dan dipakai produksi; status resmi menunggu keputusan Pemilik (bukan saya). Saran: Pemilik menandai ADR yang berjalan sebagai Accepted, atau menetapkan mana yang dibatalkan.

| ADR | Judul | Status |
|---|---|---|
| sqlite | ADR-0001 — SQLite driver, embedded version, WAL durability, checkpoint | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| config | ADR-0002 — Configuration format and secrets loading | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| go | ADR-0003 — Go–Python transport | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| python | ADR-0004 — Python tooling | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| id | ADR-0005 — Identifier format | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| capability | ADR-0006 — Capability grant model | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| worker | ADR-0007 — Worker trust and scratch boundary (P1) | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| event | ADR-0008 — Event retention and audit/content separation | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| benchmark | ADR-0009 — Benchmark execution environment | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| browser | ADR-0010 — Browser egress boundary (contract only; implementation P4) | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| oca | ADR-0011 — Owner Communication Agent contracts (contract only; impleme | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| commercial | ADR-0012 — Commercial boundary (contract only) | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| supervisor | ADR-0013 — Stable contracts for P2 supervisor, activity events and tel | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| separate | ADR-0014 — Deployment as an independent Docker stack | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| http | ADR-0015 — HTTP server and Go dependency policy | Proposed — awaiting owner approval (P0 gate). Date: 2026-11- |
| agent | ADR-0016 — P2 agent loop: replanning, verification, plan review, clari | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| dynamic | ADR-0017 — Provenance-bound dynamic tool arguments | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| supervisor.md | ADR-0018 — Persistent Agent Supervisor v1 | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| subagents.md | ADR-0019 — Subagents v1 (sequential) and Shared Task Context | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| mcp | ADR-0020 — MCP client adapter | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| second | ADR-0021 — Second model provider and web search providers | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| console | ADR-0022 — Event stream and Console TUI | Proposed (owner decided P2→P3 without a pause, A-21). Date:  |
| memory.md | ADR-0023 — Memory with provenance (P3) | Proposed (P3, owner decision A-21). Date: 2026-11-09. |
| identity | ADR-0024 — Identity and Charter framework (P3) | Proposed (P3, owner decision A-21). Date: 2026-11-09. |
| learning | ADR-0025 — Learning L1: lessons and skills (P3) | Proposed (P3, owner decision A-21). Date: 2026-11-09. |
| owner | ADR-0026 — Owner Working Profile (P3) | Proposed (P3, owner decision A-21). Date: 2026-11-09. |
| self | ADR-0027 — Self-Model v1, efficiency scorecard, shadow routing (P3) | Proposed (P3, owner decision A-21). Date: 2026-11-09. |
| triggers | ADR-0028 — Scheduled triggers and Proactive Intelligence v1 (P3) | Proposed (P3, owner decision A-21). Date: 2026-11-09. |
| task | ADR-0029 — Task state names and the representation of INTERRUPTED / RE | Proposed (checkpoint v1.3.11, 2026-11-09). |
| limitation | ADR-0030 — Limitation Ledger v1 and the closed self-improvement loop ( | Proposed (checkpoint v1.3.11, 2026-11-09). |
| r0 | ADR-0031 — R0 runtime self-healing and workspace time zone (OL-01/02/0 | Proposed (checkpoint v1.3.11, 2026-11-09). |
| clarification | ADR-0032 — Clarification timeout policy (`INPUT_TIMEOUT`) | Proposed (2026-11-10). Implements the Blueprint §8.2 sentenc |
| per | ADR-0033 — Per-task recovery policy (`auto` / `manual` / `never`) | Proposed (2026-11-10). Implements Blueprint §8.2: "Recovery  |
| authenticated | ADR-0034 — Authenticated inbound webhooks | Proposed (2026-11-10). Blueprint §8.11 and §8.2.1 call for a |
| encrypted | ADR-0036 — Encrypted secrets at rest, key rotation, backup/restore | Proposed (P4-M2, 2026-11-10). Implements Blueprint D.1 P4: " |
| signed | ADR-0037 — Signed runtime artifacts | Proposed (P4-M3, 2026-11-10). Implements Blueprint D.1 P4: " |
| l2 | ADR-0038 — L2 tools in a WASM sandbox (remedy R3) | Proposed (P4-M4, 2026-11-10). Implements Blueprint 8A.1 L2 ( |
| workspace | ADR-0041 — Workspace Web App, owner authentication and remote exposure | Proposed (P5a, 2026-11-10). Implements Blueprint 13A.4 (Work |
| telegram | ADR-0044 — First messaging channel: Telegram bot | Proposed (P5b, 2026-11-10). Implements D.1 P5 "P5b: add one  |
| github | ADR-0045 — First connector pilot: GitHub, read-only | Proposed (P5b, 2026-11-10). Implements D.1 P5 "First connect |
| specialist | ADR-0046 — Specialist subagent profiles | Proposed (P6, 2026-11-10). Implements D.1 P6 "managed specia |
| l3 | ADR-0048 — L3 brain variants through AURA Lab (R4 remedies) | Proposed (P6, 2026-11-10). Implements these items of D.1 P6: |
| enterprise | ADR-0049 — Enterprise Admin: departments, audit search/export, usage b | Proposed (P6, 2026-11-10). Implements part of D.1 P6 "Enterp |
| aul | ADR-0050 — AUL v1: capability-safe workflows compiled to IR and run in | Proposed (P8, 2026-11-10). Implements D.1 P8: "malformed AUL |
| p9 | ADR-0051 — P9 supply-chain checks: reproducible build and license audi | Proposed (P9, 2026-11-10). Covers the parts of D.1 P9 "repro |
| android | ADR-0052 — Android client v0.1 (P7) | Proposed (P7, 2026-11-10). The owner chose "build directly w |
| app | ADR-0053 — App accounts: username/password sign-in for the Android cli | Proposed (2026-11-10). Owner request: when signing in from t |
| adaptive | ADR-0054 — Adaptive routing: route health, immediate rest on quota, ti | Proposed (2026-11-10). Owner request: AURA must manage its o |
| sandbox | ADR-0056: Sandbox stack and validation suite | accepted (owner request of 2026-11-10: "tolong ditesting di  |
| live | ADR-0057: Live voice conversation on the Android client | accepted (owner request of 2026-11-11: "buat aura bisa mende |
| understanding | ADR-0058: Understanding the owner's speech | accepted (owner request of 2026-11-11: "sempurnakan bagian o |
| living | ADR-0060: Living canvas — richer views, narrated explanations, AURA-de | accepted (owner instruction of 2026-11-11: rebuild the app's |
| workspace.md | ADR-0061: Workspace — an empty home that fills with what AURA is doing | accepted (owner instruction of 2026-11-11: home is an empty  |
| chromium | ADR-0062: Web search through Chromium, news without keys, background b | accepted (owner instruction of 2026-11-11: "Brave ganti ke C |
| everyday | ADR-0063: Everyday information tools and reminders from conversation ( | accepted (owner instruction of 2026-11-11, wave 2 of the pla |
| office | ADR-0064: Office documents — write Word, Excel and PDF; read Word, Exc | accepted (owner instruction of 2026-11-11, wave 3 of the pla |
| server | ADR-0065: Server commands in a sandbox, under a watcher, fully logged | accepted — owner decision of 2026-11-11 ("Sisi keluasan kema |
| jarvis | ADR-0066: JARVIS as a front end of AURA (AURA is its worker engine) | accepted — owner instruction of 2026-11-11 ("backend/engine  |
| code | ADR-0067 — Code work for JARVIS in the shell sandbox | ? |
| web | ADR-0068 — web.search: a plain HTTP DuckDuckGo fallback | accepted (owner instruction of 2026-11-14: "cek log JARVIS … |
| odi | ADR-0069 — ODI A1: canonical lifecycle mapping, error/event contracts, | Proposed — awaiting owner approval · Date: 2026-11-16 · Evid |
