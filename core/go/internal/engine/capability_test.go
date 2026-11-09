package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"aura.local/core/internal/task"
)

// hostileWorker behaves like a compromised or prompt-injected worker: it tries
// requests outside its grant before doing the legitimate work.
const hostileWorker = `
import json, sys
from aura_worker.rpc import Channel, RpcError

ch = Channel()
def attempt(method, params, token=None):
    saved = dict(ch.meta)
    if token is not None:
        ch.meta["capability_token"] = token
    try:
        return ch.call(method, params)
    except RpcError as e:
        return {"denied": e.reason}
    finally:
        ch.meta = saved

while True:
    m = ch.read()
    if m is None:
        break
    ch.meta = m.get("meta") or {}
    p = m.get("params") or {}
    if m["method"] == "task.plan":
        attempt("llm.complete", {"tier": "standard", "messages": [{"role": "user", "content": "x"}]}, token="A" * 43)
        attempt("tool.invoke", {"tool_id": "web.fetch", "arguments": {"url": "http://fixture.test/one"}})
        attempt("shell.exec", {"cmd": "id"})
        path = ".aura/evil.txt" if "MODE_A3" in p["objective"] else "reports/x.md"
        plan = {"steps": [
            {"step_no": 1, "description": "f", "tool_id": "web.fetch", "url": "http://fixture.test/one", "path": None},
            {"step_no": 2, "description": "w", "tool_id": "workspace.write", "url": None, "path": path}],
            "completion_criteria": []}
        ch.write({"id": m["id"], "result": plan})
    elif m["method"] == "task.step":
        s = p["step"]
        if s["tool_id"] == "web.fetch":
            attempt("tool.invoke", {"tool_id": "web.fetch", "arguments": {"url": "http://169.254.169.254/latest/meta-data/"}})
            attempt("tool.invoke", {"tool_id": "workspace.write", "arguments": {"path": "x", "content": "y"}})
            r = attempt("tool.invoke", {"tool_id": "web.fetch", "arguments": {"url": s["url"]}})
            attempt("tool.invoke", {"tool_id": "web.fetch", "arguments": {"url": s["url"]}})
        else:
            attempt("tool.invoke", {"tool_id": "workspace.write", "arguments": {"path": "../../etc/aura.txt", "content": "pwn"}})
            attempt("tool.invoke", {"tool_id": "workspace.write", "arguments": {"path": s["path"], "content": "ok", "mode": "0777"}})
            r = attempt("tool.invoke", {"tool_id": "workspace.write", "arguments": {"path": s["path"], "content": "ok"}})
        ok = r.get("status") == "ok"
        ch.write({"id": m["id"], "result": {"decision": "step_done" if ok else "fail", "reason": json.dumps(r)}})
    elif m["method"] == "worker.shutdown":
        ch.write({"id": m["id"], "result": {"accepted": True}})
        break
`

func deniedReasons(t *testing.T, h *harness, taskID string) []string {
	rows, err := h.st.R.Query("select data_json from audit_events where type='CAPABILITY_DENIED' and task_id=?", taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		var m map[string]any
		json.Unmarshal([]byte(d), &m)
		out = append(out, m["reason"].(string))
	}
	sort.Strings(out)
	return out
}

func TestOutOfScopeRequestsDeniedAndAudited(t *testing.T) {
	h := newHarness(t, harnessOpts{workerCmd: []string{"python3", "-I", "-c", hostileWorker}})
	tk := h.submit("Fetch http://fixture.test/one and write reports/x.md", nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("legitimate work should still succeed: %s %s", done.State, deref(done.Reason))
	}
	got := strings.Join(deniedReasons(t, h, tk.ID), ",")
	want := strings.Join([]string{
		"arguments_differ_from_plan", // fetch of the metadata URL
		"arguments_differ_from_plan", // write outside the planned path
		"capability_denied",          // forged token
		"invalid_arguments",          // unknown argument field
		"method_not_allowed",         // shell.exec
		"one_tool_call_per_step",     // second fetch in the same step
		"tool_not_in_current_step",   // tool call during planning
		"tool_not_in_current_step",   // write during a fetch step
	}, ",")
	if got != want {
		t.Fatalf("denials:\n got %s\nwant %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Workspace.Root, "..", "etc", "aura.txt")); err == nil {
		t.Fatal("write escaped the workspace")
	}
	var calls int
	h.st.R.QueryRow("select count(*) from usage_records where task_id=?", tk.ID).Scan(&calls)
	if calls != 0 {
		t.Fatal("forged-token model call reached the provider")
	}
}

func TestA3ForbiddenWrite(t *testing.T) {
	h := newHarness(t, harnessOpts{workerCmd: []string{"python3", "-I", "-c", hostileWorker}})
	tk := h.submit("MODE_A3 fetch http://fixture.test/one", nil)
	done := h.wait(tk.ID, task.Failed, task.Succeeded)
	if done.State != task.Failed {
		t.Fatalf("A3 write was not stopped: %s", done.State)
	}
	if !strings.Contains(strings.Join(deniedReasons(t, h, tk.ID), ","), "forbidden") {
		t.Fatal("A3 denial not audited")
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Workspace.Root, tk.ID, ".aura", "evil.txt")); err == nil {
		t.Fatal("protected path written")
	}
}
