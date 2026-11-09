# CLAUDE.md

Claude Code is the sole implementation coding agent for AURA (Blueprint OD-8). The human Product Owner/Architect is the final authority; Claude Code is Lead Developer and Principal Implementation Engineer.

The canonical engineering rules are in `AGENTS.md`, loaded here:

@AGENTS.md

If anything in this file appears to conflict with `AGENTS.md`, `AGENTS.md` wins. If the import above was not loaded, read `AGENTS.md` before doing anything else.

Working notes for Claude Code:

1. Follow the read order in `AGENTS.md` Section 1 and the per-phase reading guide in `docs/MASTER_BLUEPRINT.md` Section 0.
2. Use Plan Mode before changing files. Present the plan, the files to change, and any scope questions, then wait for approval.
3. Read `docs/MODEL_ASSIGNMENT.md` before each milestone and use the recommended build model. State the model(s) actually used in the completion report.
4. Security-critical milestones get an independent review in a separate, fresh session before merge.
5. Never fabricate test or benchmark results. A test counts as passed only if it was executed and its evidence inspected.
6. Do not edit owner-controlled documents (list in `AGENTS.md` rule 10). Propose changes only when the owner asks.
7. The blueprint is frozen for new scope until Phase 1 is accepted; put new ideas in `docs/PROPOSALS.md`.
