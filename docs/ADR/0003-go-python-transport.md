# ADR-0003 — Go–Python transport

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision
JSON-RPC 2.0, newline-delimited, over the worker's stdin/stdout; stderr is worker log (captured, size-capped,
redacted). Schemas in `contracts/schemas/` (draft 2020-12) are the single contract; Go and Python both run contract
tests against them. Max message 4 MiB; larger payloads are refused, not truncated.

Bidirectional: Core→Worker calls (`task.plan`, `task.step`, `worker.health`, `worker.shutdown`) and Worker→Core
requests (`llm.complete`, `tool.invoke`, `task.checkpoint`, `approval.request`) share one stream, distinguished by
`id` prefix (`c-` Core-originated, `w-` worker-originated). Every envelope carries task, step, trace,
correlation, capability token, deadline and cancel generation.

**Concurrency (P1):** one worker, **one task at a time** (serialized). Interleaving is not claimed until tested (§9).

## Alternatives
gRPC/Protobuf — rejected for P1 (extra toolchain, no measured need). HTTP on loopback — rejected: gives the worker a
network socket, contrary to §5.1 rule 3.
