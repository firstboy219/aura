# ADR-0014 — Deployment as an independent Docker stack

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Context
Owner: run AURA in its own Docker stack so it disturbs nothing else on the host. Blueprint §12: co-resident services must be
measured and AURA must enforce its own quotas; §15: AURA is independent of every other product.

## Decision
- Compose project `aura` (`deploy/docker-compose.yml`): one service `aura-core` (Go Core; spawns the Python worker
  as a child process inside the container), network `aura-net`, volume `aura-data`. Host bind
  `127.0.0.1:7400` only. No reverse-proxy registration; remote access later only by explicit owner decision.
- Host config/secrets under `/opt/aura/` (config.yaml 0644, secrets/ 0700 with files 0400), owned by uid 10001.
- Limits: 1 CPU, 1 GiB RAM, 256 pids; read-only rootfs; `cap_drop: ALL`; `no-new-privileges`.
- Build/test only through throwaway containers and AURA-only cache volumes (`aura-gomod`, `aura-uvcache`).
- Starting/restarting `aura-core` happens only on an explicit owner command.

## Consequences
No shared failure domain with other products except host CPU/RAM/disk. **Open conflict (see ASSUMPTIONS A-04):** the OpenRouter key currently in use is a copy of a key owned by another product, which Blueprint §15 / AGENTS rule 9 do not allow; the owner should give AURA its own key. Docker isolation
is a deployment boundary, not the P4 sandbox.
