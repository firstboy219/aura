# ADR-0066: JARVIS as a front end of AURA (AURA is its worker engine)

Status: accepted — owner instruction of 2026-11-11 ("backend/engine Jarvis ... diganti menjadi aura, sehingga Jarvis bertindak sebagai
frontend dan aura sebagai backend"); the JARVIS side is its ADR-089.

## Decision
- JARVIS is a **client of AURA's public API** (AGENTS rule 17), nothing more: it creates tasks (`POST /api/v1/tasks`, `data_class` from
  its private flag, an `Idempotency-Key` per task), reads their events and replies, answers questions, decides approvals and cancels.
  AURA does not call, read or depend on JARVIS (rule 9 holds: no runtime dependency in this direction).
- JARVIS authenticates as a **member** named `jarvis` with role **approver** (can create tasks and decide approvals, cannot change
  settings, secrets, members or the system). The owner creates it with `sudo deploy/member-token.sh jarvis approver
  ~/my-project/infra/data/aura/token ubuntu`; the token is written to that file only. Approvals decided through JARVIS are recorded as
  `member:jarvis (approver)`; JARVIS asks the owner with the phone's lock before it decides.
- Network: JARVIS' core joins `aura-net`; AURA's API there is reachable by name `aura-core:7400`. The browser and shell networks stay
  refused by `server.deny_cidrs`.

## Consequences
- JARVIS' tasks appear in AURA's task list and logs like any task; budgets, the kill switch, the watcher and the shell log apply.
- Disabling the member (`POST /api/v1/admin/members/{id}/disable`) cuts JARVIS off at once; JARVIS then falls back to Hermes.
