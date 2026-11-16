# ADR-0074 — ODI A5: tool manifests, per-tool quotas, isolation evidence

Status: **Proposed** — awaiting owner approval · Date: 2026-11-16 · Evidence: unit/engine tests and a live probe (implementation-verified)

## Context
Blueprint v1.1 §10–§11. A0 found: compiled tools have a trusted entry (id, version, side effect, tier) but no declared network destination or data classes; tool quotas did not exist; worker isolation was strong (network none, read-only, cap_drop ALL) and already had a probe script, but no recorded evidence.

## Decision (no schema change)
1. **Manifest registry** (`internal/odi.ToolManifest`, `GET /api/v1/admin/tool-manifests`). Derived from the trusted entry plus facts declared for the compiled tools: network destination class (`none | task_allowlist | fixed_public_sources | sandbox_egress_proxy | declared_by_owner`) and data classes. mcp./l2./connector. tools get owner-declared defaults. `Validate` is the registration check. A test fails if a compiled tool has no declared facts (so a tool cannot be added silently) or a declaration refers to a tool that no longer exists. Timeouts and retries are deliberately **not** listed: they are not per-tool facts in AURA.
2. **Per-tool daily quotas** (`odi.tool.quotas` setting; `GET/PUT /api/v1/admin/tool-quotas`, audited). `serveTool` refuses the call that would exceed it (`tool_quota_exceeded`, canonical reason BUDGET_EXCEEDED), counting the tool's steps that started today and not the asking step. No row means unchanged behaviour. A quota that cannot be read is not enforced: it is a cost control, not an authority.
3. **Isolation evidence.** `deploy/checks/worker-isolation.sh` was run against the live `aura-worker` on 2026-11-16 and passed (kept in `docs/odi/evidence/`); AT-05 is recorded as met. It must be re-run after every deploy.
4. **Worker protocol.** JSON-RPC 2.0 with a schema per method (`contracts/schemas`, tested), strict decoding and a rejected envelope without `jsonrpc: "2.0"` and an id; Core and worker ship from the same commit. No separate protocol version field was added (it would change the Python worker); revisit if they ever ship separately.

## Not done
No per-tool health check (compiled tools have none; MCP health is the MCP client's), no manifest signature (L2 tools are bound to the approved source's sha256), no change to isolation (the owner decides whether seccomp/AppArmor/Landlock/microVM are wanted on top).

## Rollback
Revert the commit; no migration. Remove the quota row to lift all quotas.
