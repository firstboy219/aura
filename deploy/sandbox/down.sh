#!/usr/bin/env bash
# Stop the sandbox and delete its data.  sudo deploy/sandbox/down.sh [--keep-data]
set -euo pipefail
cd "$(dirname "$0")"
if [ "${1:-}" = "--keep-data" ]; then docker compose down; else docker compose down -v; fi
