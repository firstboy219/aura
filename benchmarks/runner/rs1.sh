#!/usr/bin/env bash
# RS-1 real-provider acceptance runner (opt-in). Pages are public, so the task is marked --public and may use
# free model routes (cost USD 0); with paid routes a run costs ~USD 0.03–0.08.
# Runs N times against the deployed aura-core container and checks each result.
# Usage: sudo benchmarks/runner/rs1.sh [runs=10]
set -euo pipefail
RUNS=${1:-10}
URLS=("https://example.com/" "https://www.iana.org/help/example-domains" "https://www.rfc-editor.org/rfc/rfc2606.txt")
OBJ="Fetch ${URLS[0]}, ${URLS[1]} and ${URLS[2]}. Summarize each page in at most 150 words and write reports/summary.md with the source link on its own line directly under each summary."
OUT=$(mktemp -d)
pass=0
for i in $(seq 1 "$RUNS"); do
  id=$(docker exec aura-core aura task create "$OBJ" --public --tool web.fetch --tool workspace.write \
       --allow-domain example.com --allow-domain www.iana.org --allow-domain www.rfc-editor.org | awk '{print $1}')
  for _ in $(seq 1 300); do
    state=$(docker exec aura-core aura task show "$id" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["state"])')
    case "$state" in SUCCEEDED|FAILED|CANCELLED|BUDGET_EXCEEDED|PAUSED|WAITING_APPROVAL) break;; esac
    sleep 2
  done
  docker exec aura-core aura task show "$id" > "$OUT/$i-task.json"
  docker exec aura-core aura trace "$id" > "$OUT/$i-trace.txt" || true
  docker exec aura-core cat "/var/lib/aura/workspace/$id/reports/summary.md" > "$OUT/$i-summary.md" 2>/dev/null || true
  if python3 -I - "$OUT/$i-task.json" "$OUT/$i-summary.md" "${URLS[@]}" <<'PY'
import json, sys
task = json.load(open(sys.argv[1]))
text = open(sys.argv[2]).read() if task["state"] == "SUCCEEDED" else ""
urls = sys.argv[3:]
problems = []
if task["state"] != "SUCCEEDED":
    problems.append(f"state {task['state']} ({task.get('reason')})")
pos = []
for u in urls:
    i = text.find(u.rstrip("/"))
    if i < 0:
        problems.append(f"missing source link {u}")
    pos.append(i)
if not problems:
    order = sorted(zip(pos, urls))
    prev = 0
    for p, u in order:
        words = len(text[prev:p].split())
        if words > 170:  # 150-word summary plus a heading line
            problems.append(f"summary before {u} has {words} words")
        prev = p + len(u)
print(f"run cost ${task.get('cost_usd', 0):.4f}: " + ("PASS" if not problems else "FAIL " + "; ".join(problems)))
sys.exit(1 if problems else 0)
PY
  then pass=$((pass+1)); fi
done
echo "RS-1 real provider: $pass/$RUNS passed (artifacts in $OUT)"
[ "$pass" -ge $(( RUNS * 9 / 10 )) ]
