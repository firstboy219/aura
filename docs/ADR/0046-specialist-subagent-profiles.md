# ADR-0046 — Specialist subagent profiles

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Profiles are owner configuration**, defined in `specialists:` in `config.yaml`. Each profile has:
  - name;
  - description (shown to the planner);
  - `tools` (the most the profile may use);
  - optional model `tier`;
  - `instructions` (at most 2000 characters).
  AURA cannot create or edit profiles at runtime. They are L4-adjacent configuration that only the owner changes.
- **Same governance as any subagent:**
  - The child's grant is the **intersection** of the profile's tools and the parent's grant, so a profile never widens permissions.
  - The child's budget is carved from the parent's budget.
  - Cancel and pause cascade.
  - Findings are posted to the shared task context with provenance.
  - Only the main agent talks to the owner.
- **Identity:** a profile runs under the same AURA identity and Charter. Its instructions are added to the child's briefing as
  owner-authored guidance; they cannot override the Core's policy.
- **Unknown profile:** a `delegate` step that names an unknown profile fails validation and is replanned. It is not silently
  run as a generic child.

## Alternatives
- Free-form agent personas written by the model: rejected, because it would let model output define permissions.
- Separate identities per specialist: rejected by rule 18 ("One Mind, Many Hands").

## Evidence
`engine/specialists_test.go` checks that:
- the child receives the profile instructions and tier;
- a tool in the profile but not in the parent's grant is not granted;
- the planner sees the profile list.

## Limits
- No profiles are configured in production yet; the owner decides which specialists exist.
