# ADR-0079 — Heavy-task overflow to external AI (owner decision, option A)

Status: Accepted by the owner 2026-11-17 ("Saya pilih A"). Amends the "only two fallback triggers" rule of ADR-0073/0075 (Blueprint §7), by the owner's authority.

## Context
Switching AURA to NEXUS-primary was tried on 2026-11-17 and every task failed at once: even a one-word question becomes a ~4.7k-token planning prompt (tool manifests, rules) and the
NEXUS mini's context is 4096 tokens (`input does not fit the model context (about 4662 + 2048 tokens > 4096)`); on 2 CPU cores a long agent call also takes minutes. The owner had already
said heavy tasks may use an external AI and NEXUS must learn from them.

## Decision
- A third evidence-based trigger, `NEXUS_CAPACITY_EXCEEDED`: in primary mode, when NEXUS is healthy but **cannot take** a call — it refuses the input as not fitting its context, or it does not
  finish within `overflow_timeout_seconds` (default 45) — the call is served by an external AI.
- **Off by default**; only the owner's policy switch `overflow_to_external` turns it on. Without it, primary mode behaves exactly as before (NEXUS answers or says so).
- It is **not an outage**: the health monitor is not told, so NEXUS stays HEALTHY and keeps serving what fits.
- Everything else of the fallback path stays: PUBLIC data only (private/internal calls are refused, never sent out), approved providers only, **free routes first and unlimited; the limits (budget,
  call count) apply to paid routes only**, an authorization per task+purpose with the evidence of the refusal, audit rows (`FALLBACK_AUTHORIZED`, `FALLBACK_USED`), no chaining of paid allowances.
- Only a *capacity* refusal qualifies: an ordinary bad request (`INVALID_REQUEST` without the context marker), "insufficient evidence", "overloaded" or an uncertain answer are **not** triggers.
- NEXUS learns from it: the external answer of a public task is captured as a teaching example (ADR-0078); short ones go to NEXUS as lessons, long ones (> 9000 characters) are kept here as
  fine-tuning candidates.
- Migration 0026 widens the CHECK of `fallback_authorizations.trigger_code` (table rebuilt; it holds no rows that other tables reference).

## Consequences
- Tasks work in primary mode on this hardware; NEXUS serves the calls that fit and finish in time, the rest go out under the limits above. How much NEXUS actually serves is measured
  (`/admin/nexus` shadow/usage views), not assumed.
- The blueprint invariant "no silent delegation to external models" is kept in spirit: every external call is explicit, owner-enabled, public-only, audited and counted.
- Reversible: `overflow_to_external=false` or `mode=shadow`.
