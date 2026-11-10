# ADR-0056: Sandbox stack and validation suite

Status: accepted (owner request of 2026-11-10: "tolong ditesting di sandbox")

## Context
Unit and integration tests use a fake provider. They cannot show how AURA behaves with real model routes, and
production must not be the place to find out. The eval suite (`benchmarks/eval`) runs inside the production Core.

## Decision
- `deploy/sandbox/`: a second compose project `aura-sbx` built from the working tree: its own containers, network,
  volumes and port (127.0.0.1:7410). No tunnel, browser, web origin, channels, connectors or Lab; unsigned image
  allowed; daily budget 0.50 USD. Its config is derived from the production config (same routes); provider keys are
  files placed by the operator in `/opt/aura-sbx/secrets`. Start `sudo deploy/sandbox/up.sh`, stop `down.sh`.
- `benchmarks/sandbox/`: twelve end-to-end scenarios with deterministic checks on the task and its reply (state, notes,
  view block types, words that must or must not appear, files, privacy guard). Every scenario content is public and
  synthetic. Wording quality is not scored. Run `sudo benchmarks/sandbox/run.sh`; reports go to `benchmarks/reports/`.
- Rules checked for every scenario: a failed task has a reply labelled `task_failed`; a completed one never has it. A
  private task must make zero attempts on a zero-price route.

## Alternatives
- Test in production with a flag: failures would reach the owner's channels and records.
- Record and replay provider responses: stable, but it stops measuring the real routes, which is the point here.

## Consequences
- Results depend on free-route availability and vary between runs; a report is a measurement of one run, not a
  guarantee. Reports state the run time, build and pass count as measured.
- The sandbox shares the provider quotas with production.
- The suite is not a comparison with other agents; no such claim is made from it.

## Evidence (2026-11-10, free routes, implementation-verified)
| Run (UTC) | Passed | Note |
|---|---|---|
| 12:54 | 11/12 | follow-up failed with TOOL_FAILED and no reply |
| 13:12 | 11/12 | planner asked the owner what they had just asked |
| 13:17 | 11/12 | follow-up answered about the oldest exchange |
| 13:22 | 12/12 | after the fixes in ADR-0054/0055 addenda |
