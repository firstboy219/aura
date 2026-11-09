# Eval run aura-eval-v1 — 20261109T233838

**Success: 0/6** · wall time p50 54.3 s, p90 112.5 s · model calls total 7 · cost $0.0000 · replans 0

| Task | Category | Result | State | Wall s | Model calls | Replans | Tiers | Notes |
|---|---|---|---|---|---|---|---|---|
| E01-rs1 | research-fixed-urls | FAIL | FAILED | 11.0 | 0 | 0 |  | state FAILED (all_routes_failed); reports/summary.md missing |
| E04-calc | exact-arithmetic | FAIL | FAILED | 14.4 | 0 | 0 |  | state FAILED (all_routes_failed); reports/tax.md missing |
| E05-csv-total | data-analysis | FAIL | BUDGET_EXCEEDED | 54.3 | 4 | 0 | fast,standard | state BUDGET_EXCEEDED (budget_exceeded_iterations) |
| E07-clarify | clarification | FAIL | FAILED | 103.0 | 2 | 0 | standard | no clarification question was asked; state FAILED (all_routes_failed); reports/page.md missing |
| E09-approval | approval | FAIL | FAILED | 112.5 | 1 | 0 | standard | no approval was requested; state FAILED (all_routes_failed); published/example.md missing |
| E16-deep-tier | tier-override | FAIL | FAILED | 112.5 | 0 | 0 |  | state FAILED (all_routes_failed); reports/deep.md missing; tier deep not used |

Routes used: openrouter/free

Wall time mean 68.0 s (includes free-model queueing and owner-simulation polling at 3 s).
