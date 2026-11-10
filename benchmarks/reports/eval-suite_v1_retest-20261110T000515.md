# Eval run aura-eval-v1 — 20261110T000515

**Success: 5/6** · wall time p50 269.6 s, p90 546.5 s · model calls total 24 · cost $0.0000 · replans 0

| Task | Category | Result | State | Wall s | Model calls | Replans | Tiers | Notes |
|---|---|---|---|---|---|---|---|---|
| E01-rs1 | research-fixed-urls | PASS | COMPLETED | 105.3 | 4 | 0 | fast,standard |  |
| E04-calc | exact-arithmetic | PASS | COMPLETED | 156.4 | 5 | 0 | fast,standard |  |
| E05-csv-total | data-analysis | FAIL | FAILED | 269.6 | 2 | 0 | standard | state FAILED (invalid_plan); reports/total.md missing |
| E07-clarify | clarification | PASS | COMPLETED | 448.4 | 5 | 0 | fast,standard |  |
| E09-approval | approval | PASS | COMPLETED | 546.5 | 5 | 0 | fast,standard |  |
| E16-deep-tier | tier-override | PASS | COMPLETED | 611.2 | 3 | 0 | deep |  |

Routes used: nvidia/nemotron-3-ultra-550b-a55b:free, nvidia/nemotron-3.5-super-vl:free

Wall time mean 356.2 s (includes free-model queueing and owner-simulation polling at 3 s).
