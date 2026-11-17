# ADR-0078 — ODI: NEXUS learns from the external answers AURA obtained for heavy public tasks

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Context
Owner direction (2026-11-17): a task that NEXUS mini cannot carry (long agent prompts; first shadow data: most AURA prompts exceed its 4096-token context and a 1k-token prompt takes about a minute on this CPU) may go to an external AI, and NEXUS mini should learn from it so that it can handle such a task next time. This is the Teacher Gate idea (ADR-0070/0073, owner amendments E1/E2) driven by AURA's own traffic.

## Decision
1. **Capture** (`nexus.Router.Capture`, off unless `teach_enabled`): after an external provider answered a call whose task is **public** (the Core marks the data class; the worker cannot), AURA keeps the messages, the response schema, the answer and the provider as a *teaching example* (`teaching_examples`, migration 0024). **Private tasks are never captured.** An example is not kept if it is longer than 12,000 characters (it could never fit a small model's context), if it contains something shaped like a secret, an e-mail address, a phone number or a long number (the Teacher Gate sanitizer), or if it is a duplicate; at most `teach_daily_max` per UTC day.
2. **Push** (`Service.PushLessons`, every other minute, only while teaching is on and the mode is not `off`): up to 20 new examples are sent to NEXUS (`POST /api/v1/knowledge/lessons`). NEXUS quarantines and validates each one and answers accepted/rejected with a reason; AURA records the outcome. AURA never serves from the lessons: only NEXUS may use them, as lowest-trust `external-ai` knowledge (NEXUS ADR-004).
3. **Heavy routing stays as it is today**: in `off` and `shadow` the external route serves everything; no call is moved to NEXUS by this ADR. A `hybrid` serving mode (light calls to NEXUS, heavy ones external) is **deliberately not built** until shadow data and a content-level quality comparison show NEXUS mini is good enough on the light calls; otherwise it could degrade tasks.

## What is and is not claimed
Lessons give a small model in-context examples; they do not change its weights. They help only when an example fits the context next to the new question. Whether they raise accuracy is to be measured (before/after on held-out public examples) before any claim. Fine-tuning on the collected set is an L2 change for later (rented GPU, owner approval, dataset-rights review of each provider's terms).

## Owner decisions
Enable `teach_enabled` (default off); the daily cap; which providers' answers may be kept (their terms on using outputs to train or condition another model must be read per provider: free OpenRouter routes may log prompts, which is why only public tasks may use them).

## Rollback
Set `teach_enabled` false (stops capture and push); revert the commit; the table stays unused.

## Amendment 2026-11-17 (owner): access goes through AURA (option A); limits apply to PAID routes only
- **Option A confirmed**: NEXUS never holds provider keys or calls an external AI itself; every external question goes through AURA's Teacher Gate or arrives as a teaching example. Measured cost of the extra hop: ≈2 ms (p90 82 ms) against a median external call of ≈2.5 s.
- **Free routes are unlimited; the limits are for paid routes.** The Teacher Gate and the fallback first try **free** routes (`RouteClass: free`) with no budget, no call count and no daily limit; only if no free route answers does the paid path run, and then it is counted against the daily limit / the authorization's call count and capped by the budget. The daily cap of teaching examples counts paid answers only (free answers are kept without a cap; storage is bounded by pruning already-sent rows beyond 20,000). Migration 0025 adds the `paid` flags.
- **Safety gates are not limits and stay**: public data class only, the sanitizer (secrets, e-mail addresses, phone numbers, key-shaped values), the evidence-based fallback triggers, quarantine and validation of lessons. Free routes may log prompts, which is exactly why only public tasks may reach them (the gateway enforces it).
- Teaching counters (kept / skipped by reason) are visible in `GET /admin/nexus` as `teaching`.
