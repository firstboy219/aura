#!/usr/bin/env bash
# Start the AURA sandbox from the working tree (P10-M6).  sudo deploy/sandbox/up.sh   |   sudo deploy/sandbox/down.sh
# The sandbox config is derived from the production config: same model routes, but file-based sandbox secrets, no web
# origin, no tunnel, no browser, no Lab, no signed-release requirement, and a small daily budget.
# Provider keys must be placed by the owner/operator in /opt/aura-sbx/secrets (openrouter_key, gemini_key; 0400).
set -euo pipefail
cd "$(dirname "$0")"
SBX=/opt/aura-sbx
mkdir -p "$SBX/secrets"
python3 -I - /opt/aura/config.yaml "$SBX/config.yaml" <<'PY'
import re, sys
src = open(sys.argv[1]).read()
# Keep everything up to the first top-level section after "approvals"; drop secrets/release/browser/web/lab.
keep, skip = [], False
for line in src.splitlines():
    m = re.match(r"^([a-z_]+):", line)
    if m:
        skip = m.group(1) in {"secrets", "release", "browser", "web", "lab", "channels", "connectors", "mcp"}
    if not skip:
        keep.append(line)
out = "\n".join(keep)
out = out.replace('"vault:openrouter"', "/run/aura-secrets/openrouter_key").replace('"vault:gemini"', "/run/aura-secrets/gemini_key")
out = out.replace("daily_cost_usd: 5.00", "daily_cost_usd: 0.50")
assert "vault:" not in out.split("# MCP servers")[0], "unexpected vault reference in sandbox config"
open(sys.argv[2], "w").write(out + "\n# sandbox: unsigned image allowed\nrelease: { required: false }\n")
PY
chown -R 10001:10001 "$SBX" && chmod 0700 "$SBX/secrets"
for k in openrouter_key gemini_key; do [ -s "$SBX/secrets/$k" ] || { echo "missing $SBX/secrets/$k" >&2; exit 1; }; done
docker compose up -d --build
for i in $(seq 1 30); do
  docker exec aura-sbx-core python3 -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:7400/api/v1/system/health', timeout=3)" 2>/dev/null && { echo "sandbox up on 127.0.0.1:7410"; exit 0; }
  sleep 2
done
docker logs --tail 30 aura-sbx-core; exit 1
