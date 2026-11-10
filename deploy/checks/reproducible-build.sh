#!/usr/bin/env bash
# Reproducible build check (Blueprint D.1 P9, ADR-0051): builds the Core binary and the AUL VM twice from scratch
# (no cache) with the same version and compares their sha256. Prints a Markdown report; exits 1 on a mismatch.
#   sudo deploy/checks/reproducible-build.sh [version]
set -euo pipefail
cd "$(dirname "$0")/../.."
VERSION=${1:-repro-check}
out=$(mktemp -d); trap 'rm -rf "$out"' EXIT
for n in 1 2; do
  docker build -q --no-cache --target core --build-arg VERSION="$VERSION" -t "aura-repro:$n" -f deploy/Dockerfile . >/dev/null
  cid=$(docker create "aura-repro:$n")
  mkdir -p "$out/$n"
  docker cp "$cid:/out/aura" "$out/$n/aura" && docker cp "$cid:/out/aulvm.wasm" "$out/$n/aulvm.wasm"
  docker rm "$cid" >/dev/null
  docker rmi -f "aura-repro:$n" >/dev/null
done
echo "| File | Build 1 sha256 | Build 2 sha256 | Same |"
echo "|---|---|---|---|"
rc=0
for f in aura aulvm.wasm; do
  a=$(sha256sum "$out/1/$f" | cut -c1-64); b=$(sha256sum "$out/2/$f" | cut -c1-64)
  same=yes; [ "$a" = "$b" ] || { same=NO; rc=1; }
  echo "| $f | \`$a\` | \`$b\` | $same |"
done
echo
echo "Commit: $(git rev-parse --short HEAD)$(git diff --quiet || echo ' (dirty)'), version string: $VERSION, $(date -u +%FT%TZ)"
exit $rc
