# ADR-0007 — Worker trust and scratch boundary (P1)

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision (P1 — honest trust level, §5.1 rule 4)
- The worker is a **constrained, trusted application process** started by Core inside the same `aura-core`
  container. It is *not* an OS sandbox in P1; no LLM-generated code or untrusted plugin runs in it.
- Environment is built from an explicit allowlist (`PATH`, `LANG`, `PYTHONPATH`, `AURA_SCRATCH`, `HOME`) and is
  never inherited, so no secret is ever in the worker environment. The worker gets no DB handle, no secret path, and
  no provider endpoint; it can only talk JSON-RPC on stdio.
- **Known P1 limitation (stated, not hidden):** inside the container the worker runs under the same uid as Core
  (`cap_drop: ALL` prevents switching uid), so at OS level it *could* read the secrets mount or `aura.db`. This is
  mitigated by architecture, review and tests (no code execution, no file-access code paths in the worker, the
  secret-scan and "worker opens no files outside scratch" tests) — not by OS enforcement. A separate unprivileged
  identity and namespaces arrive in P4 (§5.1 rule 6).
- Scratch: `<worker.scratch_root>/gen-<n>` per worker generation (in the container: `/tmp/aura-scratch`, tmpfs),
  removed when that worker exits. **Amended in P1:** there is no Core-checked 32 MiB quota; the bound is the
  container's 64 MiB `/tmp` tmpfs, and the P1 worker writes no files. A per-task quota is a P4 isolation item.
  Durable files only via `workspace.*` tools.
- Container hardening (deployment, not sandbox): read-only rootfs, `cap_drop: ALL`, `no-new-privileges`,
  pids/memory/CPU limits, loopback-only publish.

## P4 (not now)
OS-enforced isolation (namespaces, seccomp/AppArmor or bubblewrap, default-deny network) chosen by a later ADR.
