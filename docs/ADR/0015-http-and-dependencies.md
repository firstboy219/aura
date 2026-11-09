# ADR-0015 — HTTP server and Go dependency policy

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision
Go stdlib-first (§6): `net/http` with Go 1.22+ method/path patterns (no router framework), `log/slog` JSON logs,
`database/sql`. Allowed third-party modules in P1: `modernc.org/sqlite` (+ its modernc deps),
`gopkg.in/yaml.v3`, `github.com/oklog/ulid/v2`. JSON Schema validation in Go uses a small in-repo validator for
the subset our schemas use, or `github.com/santhosh-tekuri/jsonschema/v6` if the subset grows (ADR amendment).
**Amended in P1:** structured-output validation in the Core needs full draft 2020-12 support, so
`github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0) is a runtime dependency (gateway) and is also used by the
contract and OpenAPI conformance tests.
Every dependency is license-checked (MIT/BSD/Apache-2.0 only, §16.11) and listed in `docs/ARCHITECTURE.md`.
