# ADR-0023 — Memory with provenance (P3)

Status: **Proposed** (P3, owner decision A-21). Date: 2026-11-09.

## Decision
- One table `memories` (kind `fact` | `lesson`), each row: content, provenance (task id, step, source URL/path,
  evidence quote), scope `workspace` (this instance; Standard = one VM per workspace), privacy (`public` | `private`),
  status (`active` | `tentative` | `superseded` | `deleted`), version and `supersedes` link. Full-text search with
  SQLite FTS5 (external-content table kept in sync by triggers). Embeddings stay optional and off (Blueprint §6).
- Only the Core writes memory. The worker *proposes* (`task.reflect`); a proposed fact becomes `active` only if its
  evidence quote is found by the Core in that task's verified output or observations (same matcher as verification);
  otherwise it is stored `tentative` and never injected as fact (Blueprint: unsupported claims are not promoted).
- Retrieval: before planning, the Core searches active memories with the objective and passes the top results with
  their provenance and trust label (`memory`, model-derived). Memories with privacy `private` are only given to
  tasks whose content may reach the route used (private tasks never use prompt-logging routes, A-20).
- Correction creates a new version (old one `superseded`); deletion blanks content and keeps an audit row.
