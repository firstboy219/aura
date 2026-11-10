import json, statistics, sys

d = json.load(open(sys.argv[1]))
rs = d["results"]
ok = [r for r in rs if r["passed"]]
secs = sorted(r["seconds"] for r in rs if r["state"] == "COMPLETED")
print(f"# Sandbox validation — {d['suite']}\n")
print(f"- Run: {d['run']} UTC · build: `{d['version']}` · environment: sandbox stack `aura-sbx` (not production)")
print(f"- Result: **{len(ok)}/{len(rs)} scenarios passed** (measured; evidence level: implementation-verified on free model routes)")
if secs:
    print(f"- Time to reply for completed tasks: median {statistics.median(secs):.1f}s, min {secs[0]:.1f}s, max {secs[-1]:.1f}s (n={len(secs)})")
print(f"- Cost: ${sum(r['cost_usd'] or 0 for r in rs):.4f}")
print(f"- {d['note']}\n")
print("| Scenario | Result | Time | State | Basis / notes | View | Model calls (ok/all) |\n|---|---|---|---|---|---|---|")
for r in rs:
    calls = r["model_calls"]
    print(f"| {r['id']} | {'PASS' if r['passed'] else 'FAIL'} | {r['seconds']}s | {r['state']}{' (' + r['reason_code'] + ')' if r.get('reason_code') else ''} | "
          f"{r['basis'] or '-'} {','.join(r['notes'])} | {', '.join(r['view']) or '-'} | {sum(c['outcome'] == 'ok' for c in calls)}/{len(calls)} |")
bad = [r for r in rs if not r["passed"]]
if bad:
    print("\n## Failures\n")
    for r in bad:
        print(f"- **{r['id']}**: {'; '.join(r['fails'])}")
print("\n## Replies (first 600 characters)\n")
for r in rs:
    print(f"### {r['id']}\n> {r['ask']}\n\n{r['reply_text'] or '_no reply_'}\n")
