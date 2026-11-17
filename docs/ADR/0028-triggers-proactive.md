# ADR-0028 — Scheduled triggers and Proactive Intelligence v1 (P3)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Triggers** (`triggers`): interval or daily schedule, an objective template, tools, domains, data class and budget;
  the supervisor creates a normal task when due (Submit validation, budgets, grants all apply). Missed runs while the
  Core was down run once, not once per missed slot.
- **Watches** (proactive research, read-only): a list of public sources + purpose. Each run, the Core fetches each source
  itself (SSRF-guarded) and compares a content hash with the previous run: unchanged → no model call. Changed → a
  read-only task (tools `web.fetch` only; no workspace write, no messages, no browser) summarizes the change; the Core
  stores a finding (source, timestamp, observed change, confidence, relevance, suggested next step) and an owner
  notification. Instructions inside source content are data (RS-12). Connected-app watches remain gated until P5.
