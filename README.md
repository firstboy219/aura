# AURA — Autonomous Universal Runtime Agent

Proprietary agent runtime: Go Core (hands + keys) + Python worker (brain, no keys) + SQLite.
Product direction: `docs/MASTER_BLUEPRINT.md` (v1.3.11, owner-controlled). Rules: `AGENTS.md`.

**Status:** P1 accepted; P2 (agent loop, tools, supervisor, subagents, MCP, console) and most of P3 (memory, identity/Charter
framework, learning, owner profile, self-model, Limitation Ledger, triggers, proactive watch) are implemented and tested;
the P2a/P2b/P3a/P3b owner gates are waived by the owner's decision (docs/ASSUMPTIONS.md A-21). Conformance with the v1.3.11
blueprint and its open gaps: `docs/CONFORMANCE_v1.3.11.md`.

## Independent product
AURA is its own repository and its own Docker Compose project (`aura`): container `aura-core`, network `aura-net`,
volume `aura-data`, port `127.0.0.1:7400`. It has no dependency on any other product (Blueprint §15): it does not use or modify any other product's container, network,
volume, database, code or credentials. See `docs/ADR/0014-separate-docker-deployment.md`.

## Deploy (owner)
```
sudo deploy/setup-host.sh                                   # /opt/aura/config.yaml + /opt/aura/secrets (uid 10001)
# place the OpenRouter key at /opt/aura/secrets/openrouter_key, owner 10001, mode 0400
cd deploy && sudo docker compose up -d --build              # project "aura" only
sudo docker exec aura-core aura doctor
```

## Use
```
sudo docker exec aura-core aura task create "Fetch https://example.com/ and write reports/summary.md" \
     --allow-domain example.com
sudo docker exec aura-core aura task list
sudo docker exec aura-core aura trace <task-id> --follow
sudo docker exec -it aura-core aura approve <approval-id>   # shows the exact action, asks y/N
sudo docker exec -it aura-core aura stop --all              # kill switch
```

## Develop
```
sudo ./dev build     # go vet + build (dev image: Go 1.26 + Python 3.12 + uv)
sudo ./dev test      # Go + Python tests (real worker, fake provider, fixture server)
sudo ./dev lint      # gofmt + ruff
sudo ./dev image     # build aura-core:local (does not start anything)
```

## Layout
`core/go` Go Core · `intelligence/python` worker · `contracts/` OpenAPI + JSON-RPC schemas · `benchmarks/` RS-1…RS-12
scenarios, runners, reports · `deploy/` Dockerfiles, compose, config, host setup · `docs/` specs and ADRs.
