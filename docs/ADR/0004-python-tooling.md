# ADR-0004 — Python tooling

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision
Python 3.12 (`python:3.12-slim` image); `uv` for environments with a committed `uv.lock`; `ruff` for lint/format;
`pytest` for tests; `jsonschema` (dev) for contract tests. Runtime dependencies: none in P0; P1 may add
`pydantic` for schemas only if an ADR amendment justifies it. The worker runs with `python -I` (isolated mode).
