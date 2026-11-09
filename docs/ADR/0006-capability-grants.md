# ADR-0006 — Capability grant model

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision
- Core mints a **256-bit random opaque token** (`crypto/rand`, base64url) per (task, worker generation). Only
  `sha256(token)` is stored in `capability_grants`; comparison is constant-time on the hash. The token is never
  logged, never put in prompts, events, or tool output.
- A grant binds: task ID, worker PID + generation, audience `worker`, allowed tool IDs + operations, domain allowlist
  (web.fetch), workspace path prefix, budget reservation, deadline, expiry (≤ task wall limit), revocation state,
  `parent_grant_id` (nullable; stable contract for P2 subagents — child ⊆ parent enforced when P2 uses it).
- Every worker request is checked: token hash → grant → not expired/revoked → same worker generation → tool in grant →
  arguments within scopes → manifest tier. Denials are audited (`CAPABILITY_DENIED`).
- Tool tier and side-effect class come only from the **Core trusted manifest** (compiled into the binary in P1).
- Tiers: A0 auto, A2 approval, A3 forbidden. **A1 is rejected** (`tier_unsupported`) in P1.
- Approval hash: `sha256` over RFC 8785-style canonical JSON of {tool_id, tool_version, arguments, target,
  side_effect, task_id, step_id, grant_id}. Approval has expiry (default 30 min) and is single-use; any change in the
  action produces a different hash and the old approval is `invalidated`.
