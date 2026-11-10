# Eval run aura-eval-v1 — 20261110T005031

**Success: 4/4** · wall time p50 277.2 s, p90 468.4 s · model calls total 37 · cost $0.0000 · replans 3

| Task | Category | Result | State | Wall s | Model calls | Replans | Tiers | Notes |
|---|---|---|---|---|---|---|---|---|
| E08-recover-404 | replanning | PASS | COMPLETED | 83.6 | 6 | 1 | fast,standard |  |
| E09-approval | approval | PASS | COMPLETED | 226.6 | 6 | 0 | fast,standard |  |
| E11-subagents | subagents | PASS | COMPLETED | 468.4 | 21 | 2 | fast,standard |  |
| E15-verified | verification | PASS | COMPLETED | 277.2 | 4 | 0 | fast,standard |  |

Routes used: nvidia/nemotron-3.5-super-vl:free

Wall time mean 263.9 s (includes free-model queueing and owner-simulation polling at 3 s).

## Reading (Claude Code, 2026-11-10)
The four failures of run 20261110T001728 pass on the fixed build (n = 1 each, free routes, $0). Together with the 13 passes of that
run, every task of suite v1 has now passed at least once on free routes. That is **not** a 17/17 pass rate: the passes come from
different runs and builds, and each task ran once.
