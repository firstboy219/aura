# docs/odi — ODI A0/A1 material

- `AURA_A0_AUDIT.md` — read-only re-audit of 2026-11-16 (VERIFIED / INFERRED / UNKNOWN).
- `AURA_GAP_MATRIX.md` — gaps with priority and phase.
- `ADR_STATUS.md` — which ADRs are still "Proposed" (owner decision needed).
- `../../contracts/odi/` — state mapping, error catalogue, event envelope, NEXUS gateway contract, acceptance catalogue.
- Tests: `go test ./internal/odi/` (always) and `go test -tags odi_acceptance ./internal/odi/` (the executable gap list; fails by design while AT-xx are unmet).
