# Benchmark methodology (skeleton)

Evidence level: design-reviewed. Derived from Blueprint D.1 P0 and §21; detailed rules belong to
`docs/RESEARCH_BENCHMARK.md`, which is **MISSING** — weights, severities and hard-gate lists are `TBD_OWNER`.

- Every test ID has: fixtures (hashed), preconditions, procedure, assertions, evidence artifacts, severity, weight,
  repetitions. A test missing any field is not executable and is excluded from scoring.
- Record per run: AURA commit + image digest, model display name, provider API ID, route ID, parameters, tool
  manifest version, prompt/fixture hashes, budgets, hardware, co-resident load snapshot, run count, timestamps.
  Competitor versions: `TBD_AT_EXECUTION`.
- Report a Phase Score (applicable categories only; N/A excluded from denominator) and withhold the Full Product
  Score until all categories exist. Hard security/governance gates are pass/fail and cannot be offset.
- Report sample counts, pass rates, confidence intervals where meaningful, and failure examples. Provider/network
  latency is reported separately from AURA runtime overhead.
