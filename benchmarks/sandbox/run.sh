#!/usr/bin/env bash
# Run the sandbox scenario suite against the SANDBOX stack (deploy/sandbox/up.sh), never production.
# Usage: sudo benchmarks/sandbox/run.sh [suite.json]   → benchmarks/reports/sandbox-<suite>-<ts>.{json,md}
set -euo pipefail
cd "$(dirname "$0")"
SUITE=${1:-suite.json}
RUN=$(date -u +%Y%m%dT%H%M%S)
OUT=../reports/sandbox-$(basename "$SUITE" .json)-$RUN
SPEC_LITERAL=$(python3 -I -c 'import sys; print(repr(open(sys.argv[1]).read()))' "$SUITE")
{ echo "import json"; echo "SPEC = json.loads($SPEC_LITERAL)"; echo "RUN = \"$RUN\""; cat run_suite.py; } \
  | docker exec -i aura-sbx-core python3 -I - > "$OUT.json"
python3 -I report.py "$OUT.json" > "$OUT.md"
echo "$OUT.md"
