# ADR-0024 — Identity and Charter framework (P3)

Status: **Proposed** (P3, owner decision A-21). Date: 2026-11-09.

## Decision
- **Identity**: a Core-owned statement (name AURA, role, owner, honesty statement from Blueprint §4, non-goals from §3.1)
  is prepended to every worker system prompt by the Core-provided `identity` parameter. It is compiled into the Core,
  not stored in memory, and cannot be changed by memory, skills or model output.
- **Charter**: `charter.file` + `charter.sha256` in config. The Core reads the owner-authored file read-only at startup;
  a hash mismatch refuses startup; a missing file is reported (`doctor`, health check `charter: not_provided`) and the
  Blueprint's own binding rules (no fatwa, cite and verify religious sources, disclose uncertainty, refer to scholars)
  apply as the baseline. The Charter text is injected as the highest-priority rule block. The Charter is never written
  by AURA: memory/skill proposals whose content targets policy, permissions, identity or the Charter are rejected and
  audited (GOV-06). GOV-01…05 evaluation cases require the owner's Charter and an approved source and are pending.
