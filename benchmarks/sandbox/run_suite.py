# Runs inside the sandbox Core container (never production): SPEC and RUN are prepended by run.sh.
import json, sqlite3, subprocess, sys, time

def ask(s):
    cmd = ["aura", "ask", "--json", "--wait", "6m"]
    if s.get("public"):
        cmd.append("--public")
    for t in s.get("tools", []):
        cmd += ["--tool", t]
    for d in s.get("domains", []):
        cmd += ["--allow-domain", d]
    t0 = time.time()
    p = subprocess.run(cmd + [s["ask"]], capture_output=True, text=True, timeout=420)
    dt = round(time.time() - t0, 1)
    try:
        return json.loads(p.stdout), dt, ""
    except Exception:
        return None, dt, (p.stderr or p.stdout).strip()[-300:]

def flat(reply):
    parts = [reply.get("text", "")]
    for b in reply.get("view") or []:
        parts += [b.get("title", ""), b.get("text", "")] + (b.get("columns") or [])
        for r in b.get("rows") or []:
            parts += r
        for it in b.get("items") or []:
            parts += [it.get("label", ""), it.get("value", ""), it.get("note", "")]
    return "\n".join(parts)

def check(s, task, err, db):
    e, fails = s["expect"], []
    if task is None:
        return ["no task result: " + err]
    reply = task.get("reply")
    types = [b.get("type") for b in (reply or {}).get("view") or []]
    notes = [n.get("code") for n in (reply or {}).get("notes") or []]
    text = flat(reply) if reply else ""
    rows = db.execute("select route_id, outcome from usage_records where task_id=?", (task["id"],)).fetchall()
    if "state" in e and task.get("state") != e["state"]:
        fails.append(f"state {task.get('state')} ({task.get('reason_code')}), expected {e['state']}")
    # Every ending speaks to the owner: a failed task explains itself and says so; a completed one never carries that label.
    if task.get("state") in ("FAILED", "BUDGET_EXCEEDED") and not (reply and reply.get("text", "").strip() and "task_failed" in notes):
        fails.append("failed task has no reply labelled task_failed")
    if task.get("state") == "COMPLETED" and "task_failed" in notes:
        fails.append("completed task is labelled task_failed")
    if e.get("reply") and not (reply and reply.get("text", "").strip()):
        fails.append("no reply text")
    if "note" in e and e["note"] not in notes:
        fails.append(f"note {e['note']} missing")
    if "no_note" in e and e["no_note"] in notes:
        fails.append(f"note {e['no_note']} present")
    if "view" in e and e["view"] not in types:
        fails.append(f"view has no {e['view']} block: {types}")
    if "view_any" in e and not set(e["view_any"]) & set(types):
        fails.append(f"view has none of {e['view_any']}: {types}")
    if "chart_points" in e:
        n = max([len([i for i in b.get("items") or [] if "number" in i]) for b in (reply or {}).get("view") or [] if b.get("type") == "chart"] or [0])
        if n != e["chart_points"]:
            fails.append(f"chart has {n} numeric points, expected {e['chart_points']}")
    for k in ("text_any", "all_any", "all_any2"):
        if k in e and not any(w.lower() in (reply.get("text", "") if k == "text_any" and reply else text).lower() for w in e[k]):
            fails.append(f"none of {e[k]} in the reply")
    if "all_none" in e:
        for w in e["all_none"]:
            if w.lower() in text.lower():
                fails.append(f"forbidden phrase present: {w}")
    if "files" in e and len((reply or {}).get("files") or []) < e["files"]:
        fails.append("no file reported")
    if e.get("private_guard"):
        # No attempt at all on a zero-price (free, prompt-logging) route: an attempt already sends the prompt.
        free = {r[0] for r in db.execute("select route_id from model_tiers where price_in_per_mtok=0 and price_out_per_mtok=0")}
        sent = [r[0] for r in rows if r[0] in free]
        if not free:
            fails.append("cannot verify: no free route configured")
        if sent:
            fails.append(f"a private task was sent to a free route: {sent}")
    if e.get("not_direct"):
        # Without a fetch grant AURA must not pretend it read the page: either it does not complete, or it says so.
        if task.get("state") == "COMPLETED" and "model_knowledge" not in notes and not any(
                w in text.lower() for w in ("tidak dapat", "tidak bisa", "tidak memiliki", "cannot", "can't", "unable", "izin", "akses")):
            fails.append("completed as if the page had been read")
    return fails

db = sqlite3.connect("file:/var/lib/aura/aura.db?mode=ro", uri=True)
version = subprocess.run(["aura", "version"], capture_output=True, text=True).stdout.strip()
out = {"suite": SPEC["name"], "run": RUN, "version": version, "note": SPEC["note"], "results": []}
for s in SPEC["scenarios"]:
    task, dt, err = ask(s)
    fails = check(s, task, err, db)
    reply = (task or {}).get("reply") or {}
    calls = db.execute("select route_id, outcome, latency_ms, purpose from usage_records where task_id=? order by created_at", ((task or {}).get("id", ""),)).fetchall()
    out["results"].append({
        "id": s["id"], "ask": s["ask"], "public": bool(s.get("public")), "passed": not fails, "fails": fails, "seconds": dt,
        "task_id": (task or {}).get("id"), "state": (task or {}).get("state"), "reason_code": (task or {}).get("reason_code"),
        "cost_usd": (task or {}).get("cost_usd"), "basis": reply.get("basis"), "notes": [n.get("code") for n in reply.get("notes") or []],
        "view": [b.get("type") for b in reply.get("view") or []], "reply_text": (reply.get("text") or "")[:600],
        "model_calls": [{"route": c[0], "outcome": c[1], "ms": c[2], "purpose": c[3]} for c in calls]})
    print(s["id"], "PASS" if not fails else "FAIL " + "; ".join(fails), f"{dt}s", file=sys.stderr, flush=True)
json.dump(out, sys.stdout, ensure_ascii=False, indent=1)
