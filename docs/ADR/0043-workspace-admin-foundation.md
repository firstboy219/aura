# ADR-0043 — Workspace Admin foundation: members, roles, budget, write-only credentials

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
credentials (write-only) work with server-side authorization" and the web security test "a hidden-UI action is still denied
server-side for an unauthorized role". There is still one workspace; tenants and SSO are P6+.

## Decision
- **Roles:** owner (the bearer token file and the Web App session), plus members with the roles viewer, operator and approver.
  - Each member has an API token `aura_m_…`, shown once and stored as a hash, which can be disabled.
  - Member tokens work as bearer tokens on the API hosts. The Web App remains owner-only.
- **Authorization** is checked on the matched route pattern for every authenticated request, using one table:
  - GET routes need viewer; other methods need operator.
  - Approvals, the kill switch and limitation decisions need approver.
  - Owner only: admin, L2 tool decisions, skills, improvement runs, memory deletion, browser sessions, profiles, takeover and codes.

  Denials return 403 and are audited (`AUTHZ_DENIED`). Approvals record the member and role.
- **Daily budget:** `PUT /admin/budget` changes the governor's limit at runtime. The change is persisted in `settings` and
  applied at startup over the config value, and it is audited with the previous value.
- **Connector credentials** (`PUT/DELETE /admin/credentials/{name}`) are stored encrypted in the vault as `connector.<name>` and
  referenced as `vault:connector.<name>`. **There is no endpoint that returns a value**; the list shows names and dates only.
- CLI: `aura admin members | member add | member disable | budget [usd] | credentials | credential set --stdin | credential delete`.
- Also fixed here: improvement cycles are serialized. The RS-6 test showed that a periodic tick and an explicit run could draft
  the same tool twice.

## Evidence
`app/authz_test.go`:
- a role matrix: viewer, operator and approver against read, create, approve, kill switch, browser sessions, tools, credentials
  and memory deletion;
- denials are audited;
- a disabled member is refused;
- credentials are write-only (the list never contains the value, and there is no read route);
- the budget changes the governor and is persisted.

## Limits
- Members cannot use the Web App yet; it signs in only the owner. There is no per-member data scoping: one workspace.
- Budgets are workspace-wide; per-member budgets are not built.
