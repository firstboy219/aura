# ADR-0054 — Adaptive routing: route health, immediate rest on quota, tier rescue

Status: **Proposed** (2026-11-10). Owner request: AURA must manage its own needs instead of being rigid, and it must
answer. Evidence for the problem: on 2026-11-10 owner tasks from the Android app failed with `PROVIDER_UNAVAILABLE`
because the statically ordered free routes were exhausted while other usable routes existed.

## Decision
The gateway (`internal/gateway/health.go`) keeps a short in-memory record of how each route behaved (8 outcomes, 30
minutes) and orders candidates with it. Agent logic still names tiers only (AGENTS rule 19).
- **Immediate rest.** A 429 opens the route's circuit for 60 s (or the provider's `Retry-After`, capped at 30 minutes);
  401/402/403 opens it for 10 minutes. Repeating such a call cannot succeed, so it no longer costs five failures.
- **Demotion.** A route that failed most of its recent calls (at least 4 samples) is tried after the healthy routes of
  the same tier. Old outcomes expire, so a demoted route gets another chance.
- **Tier rescue.** When every route of the requested tier failed or is resting, routes of the other tiers are tried.
  `routing.no_tier_rescue: true` turns this off.
- **Probe.** When every candidate is resting, the one due back soonest is tried once rather than failing with no attempt.
- **Privacy first.** The data-class filter runs before all of the above: a route that may log prompts is never a
  candidate for a private task, whatever its health. Budget reservation is unchanged; a rescue to a dearer tier is still
  reserved and reconciled per call.

## Alternatives
- Keep the static order and ask the owner to edit the config when a route runs out: this is what failed.
- Persist health in SQLite: more state for little gain; a restart simply relearns in a few calls.
- A learned router (bandit): not justified by the current volume; revisit with benchmark data.

## Consequences
- A task may be answered by a different tier than requested. The usage record names the route actually used.
- Health is per process and is lost at restart.
- This does not create capacity: a private task still needs a route that does not log prompts (paid credit or a
  provider with a no-logging policy). With zero OpenRouter credit only `data_class: public` tasks can be answered.

## Evidence (implementation-verified)
`gateway_test.go`: `TestQuotaRestsRouteImmediately`, `TestUnhealthyRouteIsDemoted`, `TestTierRescue`,
`TestAllRestingStillProbes`; the existing privacy and budget tests are unchanged and pass.
