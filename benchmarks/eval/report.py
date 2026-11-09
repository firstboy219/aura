"""Render an eval run (JSON from run_eval.py) as Markdown."""
import json
import statistics
import sys

r = json.load(open(sys.argv[1]))
tasks = r["tasks"]
passed = [t for t in tasks if t.get("passed")]
walls = sorted(t["wall_s"] for t in tasks if "wall_s" in t)


def pct(xs, p):
    return xs[min(len(xs) - 1, int(round(p * (len(xs) - 1))))] if xs else 0


print(f"# Eval run {r['suite']} — {r['run']}\n")
print(f"**Success: {len(passed)}/{len(tasks)}** · wall time p50 {pct(walls, .5)} s, p90 {pct(walls, .9)} s · "
      f"model calls total {sum(t.get('model_calls', 0) for t in tasks)} · "
      f"cost ${sum((t.get('cost_usd') or 0) for t in tasks):.4f} · replans {sum(t.get('replans', 0) for t in tasks)}\n")
print("| Task | Category | Result | State | Wall s | Model calls | Replans | Tiers | Notes |")
print("|---|---|---|---|---|---|---|---|---|")
for t in tasks:
    notes = "; ".join(c["detail"] for c in t.get("checks", []) if not c["ok"])[:200].replace("|", "/")
    if t.get("error"):
        notes = str(t["error"])[:200]
    print(f"| {t['id']} | {t['category']} | {'PASS' if t.get('passed') else 'FAIL'} | {t.get('state')} | {t.get('wall_s', '')} | "
          f"{t.get('model_calls', '')} | {t.get('replans', '')} | {','.join(x for x in t.get('tiers', []) if x)} | {notes} |")
routes = sorted({x for t in tasks for x in t.get("routes", []) if x})
print(f"\nRoutes used: {', '.join(routes)}")
if walls:
    print(f"\nWall time mean {statistics.mean(walls):.1f} s (includes free-model queueing and owner-simulation polling at 3 s).")
