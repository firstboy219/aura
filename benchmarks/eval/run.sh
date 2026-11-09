#!/usr/bin/env bash
# Run eval suite v1 against the deployed aura-core (free model routes, public data).
# The runner executes inside the container so the API token never leaves it.
# Usage: sudo benchmarks/eval/run.sh [suite.json]   → benchmarks/reports/eval-<suite>-<ts>.{json,md}
set -euo pipefail
cd "$(dirname "$0")"
SUITE=${1:-suite_v1.json}
RUN=$(date -u +%Y%m%dT%H%M%S)
OUT=../reports/eval-$(basename "$SUITE" .json)-$RUN
SPEC_LITERAL=$(python3 -I -c 'import sys; print(repr(open(sys.argv[1]).read()))' "$SUITE")
{ echo "import json"; echo "SPEC = json.loads($SPEC_LITERAL)"; echo "RUN = \"$RUN\""; cat run_eval.py; } \
  | docker exec -i aura-core python3 -I - > "$OUT.json"
python3 -I report.py "$OUT.json" > "$OUT.md"
echo "$OUT.md"
