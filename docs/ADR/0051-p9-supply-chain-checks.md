# ADR-0051 — P9 supply-chain checks: reproducible build and license audit

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
dependency/license audit" that do not need owner decisions. The signed release pipeline is ADR-0037. This ADR adds the AUL
VM to the signed manifest (ADR-0050).

## Decision
- **Reproducible build check** (`deploy/checks/reproducible-build.sh`).
  - Builds the Core binary and the AUL VM twice, from scratch and with the same version string, and compares sha256.
  - Reproducibility comes from `-trimpath -buildvcs=false`, `CGO_ENABLED=0` and a pinned Go 1.26 image.
- **License audit** (`./dev audit`, `deploy/checks/license_audit.py`).
  - Classifies the license of every Go module compiled into `cmd/aura` and `cmd/aulvm` (`go list -deps`), plus the worker's
    Python runtime dependencies (none today).
  - Applies the Blueprint §16.11 policy:
    - permissive is OK;
    - LGPL, MPL and EPL need owner or legal approval;
    - GPL, AGPL and SSPL fail;
    - an unidentified license fails.
  - Aggregated third-party notice files are carried as notices, not treated as the module's license.
  - The classifier has a self-test, so it must not miss copyleft and must not mistake MPL text for the AGPL.
- **Release script** signs `aulvm.wasm` along with the binary, python.wasm and site-packages.

## Evidence
`benchmarks/reports/p9-supply-chain-20261110.md`:
- two identical builds, whose VM hash matches the production VM;
- 34 of 34 components OK, none needing approval or forbidden.

## Not decided here (owner decisions for the rest of P9)
- billing and the subscription lifecycle (provider, plans);
- license keys and offline behaviour;
- VM provisioning, fleet and recovery (cloud, regions);
- the Enterprise source package and its license controls;
- AURA-hosted operations;
- benchmark publication (what and where; AGENTS.md rule 14 forbids publishing internals).
