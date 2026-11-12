# ADR-0067 — Code work for JARVIS in the shell sandbox

**Status:** accepted (owner, 2026-11-12: "fitur yang dulu bergantung pada Hermes sekarang tolong alihkan ke AURA"). JARVIS' ADR-096.

**Context.** JARVIS' dev team and self-improvement used to run in Hermes' container on a clone of JARVIS' repository and propose changes
through an MCP tool. Hermes was removed (JARVIS ADR-093). AURA already has a watched shell sandbox (ADR-0065).

**Decision.**
- `aura-shell` mounts, read-only: JARVIS' runner mirrors of the repositories (`/mirror/upstream.git` = JARVIS main, `/mirror/autotoko.git`
  = AutoToko develop; root-owned on the host) and JARVIS' hand-off folder (`/handoff/<task>/`: the full instructions, spec, review, diffs).
  Host paths come from `JARVIS_DEV_MIRROR` / `JARVIS_HANDOFF` (defaults: the JARVIS checkout on this server).
- A JARVIS code task arrives as an ordinary AURA task whose short objective points at `/handoff/<task>/brief.md`. AURA clones the mirror in
  its task folder, edits, and returns `change.diff` (files_out → the task workspace). JARVIS reads it with `GET /tasks/{id}/files/change.diff`.
- AURA never gets write access to a repository, credentials, the Docker socket or the host. The diff is data: JARVIS' host runner applies it
  in its own staging clone (no hooks), checks it against JARVIS' policy, runs the full tests, and only the owner's approval of the exact tested
  commit merges or publishes it. The shell watcher's rules are unchanged (git push stays "review").
- Code tasks are `data_class: public` (owner decision: free models may see the code); briefs carry no personal content.

**Consequences.** The sandbox can read the whole code of JARVIS and AutoToko (not their secrets: those are not in the repositories). A
compromised AURA could read the code and write a malicious diff — which still has to pass the policy, the tests and the owner's approval.
