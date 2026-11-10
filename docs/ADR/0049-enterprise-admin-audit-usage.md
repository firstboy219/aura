# ADR-0049 — Enterprise Admin: departments, audit search/export, usage by department

Status: **Proposed** (P6, 2026-11-10). Implements part of D.1 P6 "Enterprise Admin Console supports SSO setup, role mapping,
connector governance, policy, audit search/export, and usage by department". It builds on ADR-0043 (roles, members,
write-only credentials).

## Decision
- **Departments.** A member has an optional department. It is set when the member is created or changed later by the
  owner. Changes are audited.
- **Attribution.** Each task created through the API records its creator (`tasks.created_by`: `owner` or a member ID).
  Usage is attributed to the creator of the task's *root* task, so subagents and specialists count for the person who
  asked. Work AURA started itself (schedules, improvement, Lab) is reported as `system`.
- **Usage report.** `GET /api/v1/admin/usage?group_by=department|member|day|tier|task_class|model&since&until` returns
  cost, model calls, tokens and distinct tasks. Days use the workspace time zone.
- **Audit search.** `GET /api/v1/admin/audit` filters the append-only audit log by type, actor, task, time range and a
  substring of the event data. LIKE wildcards in the query are escaped.
- **Audit export.** `format=csv|jsonl` exports up to 50,000 rows.
  - The export itself is audited (`AUDIT_EXPORTED`).
  - It is served as an attachment with `nosniff`.
  - CSV cells that start with `= + - @` are prefixed with `'`, against spreadsheet formula injection.
- **Access.** All of this is owner only, enforced server-side (routeRole). The CLI offers
  `aura admin usage|audit|member department`.

## Not in this ADR
- **SSO/OIDC setup and role mapping.** They need an identity provider and the owner's decision on which one. They are not
  built, and are reported as blocked.
- **Connector governance beyond the existing allowlist and audit (ADR-0045), and an admin policy editor.** Policies are L4,
  so changes to them stay owner and Claude Code changes under review.

## Evidence
`app/admin_p6_test.go` checks:
- usage by department and member, including a subtask counted for its root's creator and system work;
- an invalid department is refused;
- audit search with a filter and an escaped wildcard;
- CSV export: attachment, audited, formula-safe;
- approver role refused;
- a department change is reflected in the report.
