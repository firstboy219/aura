# Benchmark runner (stub)

Not implemented in P0. P1 milestone M9 adds `runner/rs1` which: starts a fixture server, creates the RS-1 task via
the REST API, waits for a terminal state, runs the scenario assertions, and writes a report from
`reports/TEMPLATE.md`. Real-provider mode only with `AURA_REAL_PROVIDER=1`.
