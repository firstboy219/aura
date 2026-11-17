# ADR-0045 — First connector pilot: GitHub, read-only

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
service principal, Core-side authorization, audit, and tenant boundary".

## Decision
- **Service principal:** a fine-grained GitHub token, read-only, limited by the owner to chosen repositories. It is stored as a
  write-only admin credential (`vault:connector.github`).
- **Tool:** `connector.github.read` (A0, read): issues, pulls, commits, or one file of a repository. The input is fixed at
  planning time and matched like MCP inputs.
- **Core-side authorization:** the repository must be in `connectors.github.repos`. This is checked before any request,
  whatever the token itself could reach. A refusal is audited (`CONNECTOR_DENIED`).
- **Audit:** every call (`CONNECTOR_CALL`: repo, kind, status, bytes).
- **Untrusted results:** responses are compacted to useful fields and become untrusted observations. Instructions inside issues
  are data.
- **Workspace boundary:** the credential and the allowlist belong to this workspace. There is no cross-workspace use (one
  workspace today).

## Evidence
`engine/connector_test.go`:
- an allowed repository is read with the scoped token, the result is summarized and the call is audited;
- the token is in no table;
- a repository outside the allowlist fails before any request reaches the API, and the refusal is audited.

## Limits
- Not yet run against the real GitHub API: no token configured.
- Read-only; write actions would need A2 approvals and are not built.
- Delegated user identity (OAuth) is not exercised; the pilot uses a scoped service principal.
