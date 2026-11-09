# ADR-0031 — R0 runtime self-healing and workspace time zone (OL-01/02/03/08)

Status: **Proposed** (checkpoint v1.3.11, 2026-11-09).

## Decision
- **Route circuit breaker**: 5 consecutive failures open a route's circuit for 60 s (doubling after a failed probe, max 15 min);
  one half-open probe call decides; `R0_CIRCUIT_OPEN/CLOSED` are audited and visible in `/self-model`. Other R0 mechanisms
  already in place: provider failover across routes and providers, bounded retry with Retry-After, worker restart, startup
  recovery, idempotent replay.
- **OL-01**: a reasoning route that returns empty content is retried once with reasoning disabled (recorded `R0_EMPTY_ANSWER_RETRY`,
  usage outcome `empty_output`); still empty → failed attempt, next route.
- **OL-02**: raw tool-call markup in a model answer is a failed attempt (usage outcome `tool_call_markup`, `R0_TOOL_CALL_MARKUP`),
  never an answer.
- **OL-05**: prompt size (characters) per model call is recorded in the `MODEL_CALLED` trace event.
- **OL-08**: `timezone` (default `Asia/Jakarta`) defines the daily budget window, trigger schedules (`daily_at` without offset) and
  improvement-budget day; tests cover 23:59–01:00 WIB. The zone database is embedded in the binary.
- **OL-03**: free routes are used only for tasks marked public (A-20); the owner chose free routes first while OpenRouter credit was
  0 — a deviation from "never default a production tier to a free endpoint", recorded in the conformance report; the breaker
  marks unhealthy routes; probing before enabling is not yet implemented.
