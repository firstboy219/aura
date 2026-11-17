# ADR-0027 — Self-Model v1, efficiency scorecard, shadow routing (P3)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Self-Model** = a report computed from Core records (not model self-assessment): per tool success rate and latency,
  per tier/route calls, invalid-output rate, latency and cost, task success / verification pass / replan rates,
  configured-but-unavailable capabilities (e.g. no search provider, no paid credit) as limitations.
- **Scorecard**: verified success rate, cost per task and per successful task, latency p50/p95, tokens, Core/worker
  RSS samples; energy = `unavailable` (no trustworthy sensor; never estimated).
- **Shadow routing**: every model call records its purpose (plan, review, verify, write, replan, pick_url, calc,
  reflect) and outcome. A learned policy picks, per purpose, the cheapest tier whose success rate is within 2 points of
  the current tier with ≥ 20 samples. Its choice is recorded beside the actual one (`routing_shadow`); it is never
  applied automatically. Promotion needs owner approval after the comparison report.
