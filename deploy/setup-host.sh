#!/usr/bin/env bash
# One-time host preparation for the aura-core container. Run by the owner:
#   sudo deploy/setup-host.sh
# Creates /opt/aura/{config.yaml,secrets/} owned by the container user (uid 10001).
# It does NOT copy any key; the owner places the OpenRouter key separately.
set -euo pipefail
cd "$(dirname "$0")"
install -d -m 0755 /opt/aura
install -m 0644 -o 10001 -g 10001 config.yaml /opt/aura/config.yaml
install -d -m 0700 -o 10001 -g 10001 /opt/aura/secrets
echo "ok: /opt/aura prepared. Place the key at /opt/aura/secrets/openrouter_key (owner 10001, mode 0400)."
