# ADR-0035 — Worker OS isolation: a separate, network-less container

Status: **Proposed** (P4-M1, 2026-11-10). Implements Blueprint D.1 P4: "Worker runs with OS-enforced isolation under a separate
unprivileged identity; network access is default-deny; direct host DB/network/secret access tests fail." It replaces the P1
limitation of ADR-0007, where the worker shared the Core's container and uid.

## Decision
- The Python worker runs in its own container, `aura-worker`, built from the same image:
  - uid/gid 10002 (the Core is 10001);
  - `network_mode: none`, so the kernel gives it only a loopback interface and no route anywhere;
  - read-only root, scratch on a 64 MiB tmpfs, `cap_drop: ALL`, `no-new-privileges`, 512 MiB / 64 pids;
  - **no** data, workspace, config or secret mounts.
- The Core reaches it only through a Unix socket on the `aura-worker-sock` volume, which only these two containers mount.
  - `aura worker-host` (in the worker container) accepts connections only from peer uid 10001, checked with `SO_PEERCRED`. Each
    connection gets a fresh worker process with its own scratch directory and an allowlisted environment.
  - When a connection ends, the host stops that process (EOF, then SIGTERM, then SIGKILL), even if the worker is busy and not
    reading its stdin.
- On the Core side the worker command becomes `aura worker-connect`, a stdio↔socket bridge. The existing supervisor is unchanged:
  on-demand start, generations bound to grants, idle shutdown, crash restart, and kill switch (killing the bridge closes the
  connection, which stops the remote worker).
- Tools, the database, secrets and the network stay in the Core, as before (ADR-0006). The worker never needed them. This ADR makes
  that architectural rule an OS-enforced one.

## Evidence
- `deploy/checks/worker-isolation.sh`, run with the worker's identity inside the container
  (`benchmarks/reports/p4-worker-isolation-20261110.md`): database, token, provider key and workspaces are absent; TCP, UDP and DNS
  fail; writes to the root filesystem fail; only `lo` exists; uid is 10002.
- `internal/workerhost` tests: the supervisor works through the host, a restart gives a new process, a busy worker is killed on
  disconnect, and another uid is refused.
- A real task (calc + workspace.write) completed through the isolated worker.

## Limits
Docker is the isolation boundary. There is no seccomp profile beyond Docker's default, no Landlock, and no user namespace remap.
Both containers share the host kernel, so a kernel exploit is out of scope. Worker RSS reported by the Core is now the bridge
process's RSS; the worker container's memory is visible with `docker stats aura-worker`.
