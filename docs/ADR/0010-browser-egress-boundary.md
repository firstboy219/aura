# ADR-0010 — Browser egress boundary (contract only; implementation P4)

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision (contract)
The P4 Playwright runtime will run as a separate on-demand process/container with **no direct egress**: all traffic
goes through a Core-controlled egress proxy that enforces the same SSRF rules as `web.fetch` (resolved-IP checks on
every connection, redirect re-validation, DNS-rebinding defense, private/loopback/link-local/metadata denial, size and
time limits). Browser sessions are per task, credentials never reach the worker/model. No implementation in P0/P1;
`web.fetch` in P1 implements the shared SSRF checker that P4 will reuse.
