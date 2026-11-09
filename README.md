# AURA — Autonomous Universal Runtime Agent

Proprietary agent runtime: Go Core (hands + keys) + Python worker (brain, no keys) + SQLite.
Product direction: `docs/MASTER_BLUEPRINT.md` (v1.3.9, owner-controlled). Rules: `AGENTS.md`.

**Status: Phase 0 (Foundation)** — documents, ADRs, contracts and a buildable skeleton. No runtime features yet;
`aura serve` and other Phase 1 commands report "not implemented".

## Separate from Jarvis
AURA is its own repository and its own Docker Compose project (`aura`): container `aura-core`, network `aura-net`,
volume `aura-data`, port `127.0.0.1:7400`. It does not use or modify any Jarvis container, network, volume,
database, or code. See `docs/ADR/0014-separate-docker-deployment.md`.

## Develop
```
sudo ./dev build     # go vet + build in golang:1.26 container
sudo ./dev test      # Go + Python tests in containers
sudo ./dev lint      # gofmt + ruff
sudo ./dev image     # build aura-core:local (does not start anything)
```

## Layout
`core/go` Go Core · `intelligence/python` worker · `contracts/` OpenAPI + JSON-RPC schemas · `benchmarks/` RS-1…RS-12
skeleton · `deploy/` Dockerfile, compose, example config · `docs/` specs and ADRs.
