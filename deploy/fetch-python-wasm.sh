#!/usr/bin/env bash
# Fetch the pinned CPython 3.12 WASI module for the L2 sandbox (ADR-0038) into .cache/ (tests) or a given path.
# Source: vmware-labs/webassembly-language-runtimes, release python/3.12.0+20231211-040d5a6. Pinned by sha256.
set -euo pipefail
cd "$(dirname "$0")/.."
DEST=${1:-.cache/python-3.12.0.wasm}
SHA=e5dc5a398b07b54ea8fdb503bf68fb583d533f10ec3f930963e02b9505f7a763
URL="https://github.com/vmware-labs/webassembly-language-runtimes/releases/download/python%2F3.12.0%2B20231211-040d5a6/python-3.12.0.wasm"
mkdir -p "$(dirname "$DEST")"
if [ -f "$DEST" ] && echo "$SHA  $DEST" | sha256sum -c --quiet 2>/dev/null; then echo "ok: $DEST"; exit 0; fi
tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
curl -fsSL -o "$tmp" "$URL"
echo "$SHA  $tmp" | sha256sum -c --quiet
mv "$tmp" "$DEST"; trap - EXIT
echo "fetched and verified: $DEST"
