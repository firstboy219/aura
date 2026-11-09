"""AURA eval suite runner (P2 acceptance: eval suite v1 >= 15 tasks).

Runs INSIDE the aura-core container (stdlib only) so the API token never leaves
it: run.sh prepends `SPEC = {...}` and `RUN = "..."` and pipes this file to
`python3 -I -`. Submits every task, plays the owner (answers questions,
approves A2 actions, sends awaited events), waits for final states, applies the
deterministic checks and prints one JSON report on stdout.
"""

import json
import re
import time
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:7400/api/v1"
TOKEN = open("/var/lib/aura/api_token").read().strip()
TERMINAL = {"SUCCEEDED", "FAILED", "CANCELLED", "BUDGET_EXCEEDED"}
SPEC = globals().get("SPEC")
RUN = globals().get("RUN", str(int(time.time())))
TIMEOUT_S = 60 * 60


def api(method, path, body=None, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            b = r.read().decode()
            return b if raw else json.loads(b or "{}")
    except urllib.error.HTTPError as e:
        if raw:
            return None
        return {"error": json.loads(e.read().decode() or "{}").get("error", {}), "status": e.code}


def fill(s):
    return s.replace("{run}", RUN) if isinstance(s, str) else s


def submit(t):
    body = {"objective": fill(t["objective"]), "tools": t["tools"], "allow_domains": t["allow_domains"], "data_class": "public"}
    for k in ("attachments", "tier", "budget"):
        if k in t:
            body[k] = t[k]
    return api("POST", "/tasks", body)


def norm(s):
    return re.sub(r"\s+", " ", s or "").lower()


def check(t, task, evs, files_text, tree, acts):
    results = []
    for c in t["checks"]:
        ok, why = True, ""
        if "state" in c:
            ok = task["state"] == c["state"]
            why = f"state {task['state']} ({task.get('reason')})"
        elif "file" in c:
            txt = files_text.get(c["file"])
            if txt is None:
                ok, why = False, f"{c['file']} missing"
            else:
                low = norm(txt)
                if "contains_all" in c:
                    ok = all(x in txt for x in c["contains_all"])
                if "contains_all_ci" in c:
                    ok = ok and all(x.lower() in low for x in c["contains_all_ci"])
                if "contains_any" in c:
                    ok = ok and any(x in txt for x in c["contains_any"])
                if "contains_any_ci" in c:
                    ok = ok and any(x.lower() in low for x in c["contains_any_ci"])
                if "not_contains_ci" in c:
                    ok = ok and not any(x.lower() in low for x in c["not_contains_ci"])
                if "in_order_ci" in c:
                    pos = [low.find(x) for x in c["in_order_ci"]]
                    ok = ok and all(p >= 0 for p in pos) and pos == sorted(pos)
                if "max_lines" in c:
                    ok = ok and len([ln for ln in txt.splitlines() if ln.strip()]) <= c["max_lines"]
                why = f"{c['file']}: {txt[:160]!r}"
        elif "no_file" in c:
            ok = c["no_file"] not in files_text
            why = f"{c['no_file']} exists" if not ok else ""
        elif "json_file_contains" in c:
            try:
                arr = json.loads(files_text.get(c["json_file_contains"], "null"))
                vals = [str(v).lower().strip(".") for v in arr]
                ok = isinstance(arr, list) and all(any(x in v for v in vals) for x in c["values_ci"])
            except ValueError:
                ok = False
            why = repr(files_text.get(c["json_file_contains"], "")[:160])
        elif "asked" in c:
            ok = acts.get("answered", False)
            why = "no clarification question was asked"
        elif "approval_requested" in c:
            ok = acts.get("approved", False)
            why = "no approval was requested"
        elif "waited" in c:
            ok = acts.get("event_sent", False)
            why = "task never waited for the event"
        elif "children_min" in c:
            ok = len(tree.get("children", [])) >= c["children_min"]
            why = f"{len(tree.get('children', []))} subagents"
        elif "no_approvals" in c:
            ok = not any(e["type"] == "APPROVAL_REQUESTED" for e in evs)
            why = "an approval was requested"
        elif "event" in c:
            ok = any(e["type"] == c["event"] for e in evs)
            why = f"no {c['event']} event"
        elif "tier_used" in c:
            ok = any(e["type"] == "MODEL_CALLED" and e["data"].get("tier") == c["tier_used"] for e in evs)
            why = f"tier {c['tier_used']} not used"
        results.append({"check": c, "ok": ok, "detail": "" if ok else why})
    return results


def main():
    tasks = {}
    for t in SPEC["tasks"]:
        r = submit(t)
        if "id" not in r:
            tasks[t["id"]] = {"spec": t, "error": r}
            continue
        tasks[t["id"]] = {"spec": t, "id": r["id"], "acts": {}, "start": time.time()}
    deadline = time.time() + TIMEOUT_S
    while time.time() < deadline:
        pending = 0
        for key, st in tasks.items():
            if "id" not in st or st.get("done"):
                continue
            task = api("GET", "/tasks/" + st["id"])
            s = task.get("state")
            t = st["spec"]
            if s in TERMINAL:
                st["done"], st["end"], st["task"] = True, time.time(), task
                continue
            pending += 1
            if s == "WAITING_INPUT" and t.get("answer") and not st["acts"].get("answered"):
                api("POST", f"/tasks/{st['id']}/input", {"answer": t["answer"]})
                st["acts"]["answered"] = True
            elif s == "WAITING_INPUT":
                api("POST", f"/tasks/{st['id']}/input", {"answer": "Use your best judgement with sensible defaults."})
                st["acts"]["unexpected_question"] = task.get("question")
            elif s == "WAITING_APPROVAL" and t.get("approve"):
                for ap in api("GET", "/approvals").get("approvals", []):
                    if ap["task_id"] == st["id"]:
                        api("POST", f"/approvals/{ap['id']}/approve", {"action_hash": ap["action_hash"]})
                        st["acts"]["approved"] = True
            elif s == "WAITING" and t.get("event") and (task.get("wait_reason") or "").startswith("event:"):
                api("POST", "/events", {"key": fill(t["event"]), "dedup_id": "eval-" + RUN})
                st["acts"]["event_sent"] = True
        if pending == 0:
            break
        time.sleep(3)
    report = {"suite": SPEC["suite"], "run": RUN, "tasks": []}
    for key, st in tasks.items():
        item = {"id": key, "category": st["spec"]["category"]}
        if "id" not in st:
            item.update(passed=False, error=st["error"])
            report["tasks"].append(item)
            continue
        task = st.get("task") or api("GET", "/tasks/" + st["id"])
        evs = api("GET", f"/tasks/{st['id']}/events").get("events", [])
        tree = api("GET", f"/tasks/{st['id']}/tree")
        files_text = {}
        for f in api("GET", f"/tasks/{st['id']}/files").get("files", []):
            files_text[f["path"]] = api("GET", f"/tasks/{st['id']}/files/{f['path']}", raw=True) or ""
        checks = check(st["spec"], task, evs, files_text, tree, st["acts"])
        calls = [e for e in evs if e["type"] == "MODEL_CALLED"]
        child_calls = 0
        for c in tree.get("children", []):
            child_calls += len([e for e in api("GET", f"/tasks/{c['task']['id']}/events").get("events", []) if e["type"] == "MODEL_CALLED"])
        item.update(task_id=st["id"], state=task.get("state"), reason=task.get("reason"),
                    passed=all(c["ok"] for c in checks), checks=checks,
                    wall_s=round(st.get("end", time.time()) - st["start"], 1),
                    model_calls=len(calls) + child_calls, replans=task.get("replans", 0),
                    tiers=sorted({e["data"].get("tier") for e in calls}),
                    routes=sorted({e["data"].get("route_id") for e in calls}),
                    cost_usd=tree.get("tree_cost_usd", task.get("cost_usd")),
                    owner_actions=st["acts"])
        report["tasks"].append(item)
    print(json.dumps(report))


main()
