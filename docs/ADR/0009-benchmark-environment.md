# ADR-0009 — Benchmark execution environment

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- Deterministic CI benchmarks run with a **fake provider** and local fixture HTTP server inside the test container.
- Opt-in real-provider runs (`AURA_REAL_PROVIDER=1`) run against OpenRouter with pinned route IDs and record: AURA
  commit, image digest, model display name, provider API ID, route ID, parameters, tool manifest version, fixture
  hashes, budgets, hardware (this host, A-12), co-resident load snapshot, run count, timestamps.
- Competitor versions are `TBD_AT_EXECUTION`; Hermes is not installed in P0/P1 (E.1).
- Scoring rules and weights come from `RESEARCH_BENCHMARK.md`, which is **MISSING**; until supplied, the skeleton
  marks weights `TBD_OWNER` and no score is published.
