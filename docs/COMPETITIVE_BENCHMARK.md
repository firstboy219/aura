# COMPETITIVE_BENCHMARK.md — Hermes / OpenClaw comparison plan

Status: **plan only**. Blueprint E.1 says this is derived from `RESEARCH_BENCHMARK.md` §5.3, which is **MISSING**;
everything that depends on it is `TBD_OWNER`. Hermes is not installed or run before the P3a comparison (E.1); AURA never calls, reads from or benchmarks against any other
product (Blueprint §15; RESEARCH_BENCHMARK §5.3 lists the only comparators: open-source agents).

## Rules (Blueprint D.1 P3, §21.3 item 7)
- First comparison at P3 on RS-1–RS-3: same pinned model route, settings, tools, budgets, fixtures; repeated runs.
- Competitor version is pinned at execution (`TBD_AT_EXECUTION`), never guessed.
- Compare only features that exist in the tested AURA phase; disclose feature differences.
- No "better than Hermes/OpenClaw" claim without evidence level, scope, date and limitations (§21.2).
- Competitors run in their own throwaway containers on a separate network, never against any other product's data.

## Metrics
Verified task success, accuracy, cost per successful task, latency p50/p90/p95 (provider separated), steps, CPU/RAM,
security hard gates (pass/fail).
