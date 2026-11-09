# ADR-0017 — Provenance-bound dynamic tool arguments

Status: **Proposed** (owner decided P2→P3 without a pause, A-21). Date: 2026-11-09.

## Context
P1 bound every tool argument to the plan (A-18). P2 research needs URLs discovered during the task (search results).

## Decision
A `web.fetch` URL that is not in the plan is allowed only if the Core itself recorded it earlier in the same task
as a `web.search` result URL or as a link extracted from a page fetched in this task (stored in a `links`
checkpoint), and it passes the domain allowlist and SSRF checks. Content cannot invent a destination; query strings
must match the recorded URL exactly (no exfiltration via appended parameters). `workspace.*` paths remain plan-bound
except files the task itself wrote. Denials are audited (`argument_without_provenance`).
