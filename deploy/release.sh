#!/usr/bin/env bash
# Build, sign and deploy a controlled AURA release (Blueprint D.1 P4, ADR-0037).
#   sudo deploy/release.sh <version>
# Signs /usr/local/bin/aura, the WASM modules (python.wasm, aulvm.wasm) and the whole Python site-packages tree of the image with the release key
# (default /opt/aura/release/signing.key, never in the repo or the image), bakes the manifest into the image,
# and restarts the stack. The Core and the worker host verify it at start against release.public_key.
set -euo pipefail
cd "$(dirname "$0")"
VERSION=${1:?usage: release.sh <version>}
KEY_DIR=${AURA_RELEASE_KEY_DIR:-/opt/aura/release}
SITE=/usr/local/lib/python3.12/site-packages
export AURA_VERSION="$VERSION"
docker compose build
# The browser image (Playwright, Chromium) is pinned upstream software and is not in the signed manifest yet (ADR-0039).
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
mkdir -p "$work/root/usr/local/bin" "$work/root$(dirname $SITE)" "$work/out"
cid=$(docker create aura-core:local)
docker cp "$cid:/usr/local/bin/aura" "$work/root/usr/local/bin/aura"
docker cp "$cid:$SITE" "$work/root$(dirname $SITE)/"
mkdir -p "$work/root/usr/local/share/aura"
docker cp "$cid:/usr/local/share/aura/python-3.12.0.wasm" "$work/root/usr/local/share/aura/"
docker cp "$cid:/usr/local/share/aura/aulvm.wasm" "$work/root/usr/local/share/aura/"
docker rm "$cid" >/dev/null
chown -R 10001:10001 "$work/out"
docker run --rm --network none --user 10001:10001 -v "$work/root:/rel:ro" -v "$work/out:/out" -v "$KEY_DIR:/key:ro" \
  --entrypoint aura aura-core:local release sign --key /key/signing.key --version "$VERSION" --root /rel --out /out/release.json \
  /usr/local/bin/aura "$SITE" /usr/local/share/aura/python-3.12.0.wasm /usr/local/share/aura/aulvm.wasm
printf 'FROM aura-core:local\nCOPY release.json /usr/local/share/aura/release.json\n' > "$work/out/Dockerfile"
docker build -q -t aura-core:local "$work/out" >/dev/null
docker compose up -d
echo "released $VERSION"
