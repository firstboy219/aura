# ADR-0069 — ODI A1: canonical lifecycle mapping, error/event contracts, acceptance catalogue

Status: **Proposed** — awaiting owner approval · Date: 2026-11-16 · Evidence: design-reviewed; tests in `core/go/internal/odi`

## Context
ODI AURA Blueprint v1.1 asks AURA to freeze its boundaries and contracts before changing behaviour (phase A1). The A0 audit (`docs/odi/AURA_A0_AUDIT.md`) found that AURA's task states, error codes and events are close to, but not the same as, the Blueprint's, and that most acceptance tests (Appendix C) are not provable yet.

## Decision
1. **Additive mapping, no behaviour change.** `contracts/odi/state-mapping.json` maps each of AURA's 12 persisted states to the Blueprint's 18 canonical states (`mapped`, `proposed` or `extension`); canonical states AURA does not have yet are listed with the phase that adds them (A2/A3). Nothing in the schema, the state machine or the API changes. `BUDGET_EXCEEDED -> FAILED(reason_code=BUDGET_EXCEEDED)` is a *proposal* for the owner. `WAITING_INPUT`, `WAITING_EXTERNAL` and `SCHEDULED` (waiting for a future time; not the Blueprint's READY) stay as documented extensions. `RECEIVED`/`VALIDATING` happen in the API before a row exists and are not persisted today (A2).
2. **Contracts as files with tests**: `error-catalogue.json` (Appendix B, 26 codes), `event-envelope.schema.json` (§14), `nexus/` (the NEXUS gateway request/result/error schemas and OpenAPI draft shared with the NEXUS project). Examples are validated in the normal test run.
3. **Acceptance catalogue as an executable gap list**: `acceptance-catalogue.json` records the honest status of AT-01…AT-12. `go test -tags odi_acceptance ./internal/odi/` fails for every test not yet met; the default test run is unaffected. A status changes only together with a real test.
4. **Drift guards**: tests fail if `tasks.state` CHECK, the `task.State` constants and the mapping disagree.

## Consequences
No production change and no deploy needed. The mapping is the compatibility layer that A2 will use to expose canonical states without breaking Jarvis or the existing API. The open owner decisions are listed in `docs/odi/AURA_A0_AUDIT.md` §7.
