# ADR-0065: Server commands in a sandbox, under a watcher, fully logged

Status: accepted — **owner decision of 2026-11-11** ("Sisi keluasan kemampuan & keamanan aura tolong dibuat menjadi
seperti hermes, saya izinkan menjalankan perintah server namun buatkan worker untuk mengawasi aura dalam melakukan
perintah server sehingga tercatat rapi di log").

## Context
The AURA vs Hermes comparison (docs/BENCHMARK_AURA_vs_HERMES.md) found AURA's main capability gap: it cannot run
commands or code, while Hermes runs any command inside its container. AGENTS.md rule 5 ("no shell tool, no eval/exec of
model output") and Blueprint §8.8 forbid this. The owner, the final authority, has now decided to allow server commands
with a watcher and a complete log. AGENTS.md is an owner-controlled document that this agent may not edit: **rule 5
remains written as it was and the owner should amend it to match this decision**; until then this ADR records the
owner's instruction as the authority for `shell.run`.

## Decision
1. **Where commands run — the `aura-shell` sandbox, not the host.** Like Hermes (which runs commands in its own
   container), AURA's commands run in a dedicated Debian container: unprivileged uid 10004, no capabilities,
   `no-new-privileges`, read-only root, pids/memory/CPU limits, no Docker socket, **no secrets and no AURA data**
   (the database, vault, worker and browser are not mounted). Each task gets `/work/<task>`; files move only when the
   Core copies them (`files_in` from, `files_out` back to the task workspace, ≤ 20 MiB each, ≤ 10 per command).
   Per command: wall-clock timeout (default 120 s, max 600 s; the whole process group is killed), CPU, address space
   (2 GiB), processes (256), file size and output (64 KiB kept of each stream) limits.
2. **Network like Hermes, minus the internal network.** `aura-shell-net` is internal (no gateway); the only way out
   is the Core's egress proxy with credentials issued per command. The session may reach **any public host** on ports
   80/443 (installing packages, public APIs, git clone); the SSRF guard still refuses private, loopback, link-local
   and metadata addresses, and the Core API refuses the shell subnet (`server.deny_cidrs`). Every host reached is
   recorded.
3. **The watcher (`internal/shellwatch`)** assesses every command before it runs, by fixed rules (no model, so it
   cannot be talked round by content the agent read):
   - **safe → runs at once (A0)**: everything not named below (reading, computing, writing in the sandbox, installing
     packages with `--user`, downloading).
   - **review → waits for the owner's approval of exactly that command (A2)**: uploads/POST/PUT/DELETE, remote logins
     and file transfer (ssh, scp, rsync, nc…), publishing (git push, npm publish, gh release…), e-mail and messaging
     endpoints, piping downloaded code into a shell, decoding hidden commands (`base64 -d | sh`, `eval`), scheduling
     (crontab), recursive permission changes.
   - **blocked → never runs (A3)**: fork bombs, wiping `/` or home, formatting devices, shutdown/reboot, reading other
     processes' environment or memory, container/host escape tooling (docker, nsenter, mount, chroot…), empty or
     over-long commands.
   Verdicts carry reasons (also notes such as "installs packages"). Credential-shaped strings in output are redacted
   before they are stored or shown to the model.
4. **The log (`shell_log`, migration 0018).** One row per command: task, step, command, verdict, reasons, status
   (refused, awaiting_approval, running, ran, timeout, error), approval id, exit code, duration, stdout/stderr (redacted,
   16 KB each kept, full byte counts), files in/out, hosts reached and egress refusals. Refusals and approval requests
   are also audit events. `GET /api/v1/shell/log`; Android: Lainnya → Perintah server.
5. **Tool `shell.run`** is granted by default when the shell is configured (`shell.socket`). The command may be planned
   or written when the step runs from earlier results (it usually depends on them); files and timeout must be as
   planned. The kill switch stops every running command.

## Alternatives
- Commands on the host itself: not what Hermes does and would expose JARVIS, AURA's data and secrets; not done.
  If the owner wants specific host diagnostics, they can be added as fixed, read-only tools.
- A model as the watcher: can be persuaded by injected text; fixed rules are predictable and testable. A model may
  later *explain* a review command to the owner, never decide it.
- Keeping rule 5: refused by the owner's decision.

## Consequences
- AURA can now do what it could not (T9 in the benchmark): run scripts, transform data, call APIs, use git.
- Executing model-written commands is a real risk inside the sandbox: a prompt injection could make AURA run
  commands that waste resources or download things; the sandbox, the network rules, the review/blocked rules and the
  log bound the damage, and nothing in the sandbox can reach AURA's data, secrets, JARVIS or the host.
- The shell and browser images are not yet covered by the signed release manifest (ADR-0037).
- `/work` keeps task directories until removed (`shell.cleanup`); a retention job is future work.
