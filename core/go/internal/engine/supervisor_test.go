package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

func waitModel(waitStep map[string]any) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "planning module"):
			return planReply(
				gatewaytest.Step(1, "write a", "workspace.write", map[string]any{"path": "reports/a.md"}),
				waitStep,
				gatewaytest.Step(3, "write b", "workspace.write", map[string]any{"path": "reports/b.md"}))
		case isSys(req, "writing module"):
			return gatewaytest.Reply{Content: `{"content":"done\n"}`}
		}
		return goodModel(r, req)
	}
}

func writesTo(evs []task.Event, path string) int {
	n := 0
	for _, e := range evs {
		var d map[string]any
		json.Unmarshal(e.Data, &d)
		if e.Type == "TOOL_SUCCEEDED" && d["tool_id"] == "workspace.write" && d["path"] == path && d["skipped"] != true {
			n++
		}
	}
	return n
}

// RS-10: wait for an external dependency, restart the Core, satisfy the
// dependency, and get exactly one wake with no duplicate side effect. The idle
// supervisor makes no model calls.
func TestRS10WaitRestartWakeOnce(t *testing.T) {
	model := waitModel(gatewaytest.Step(2, "wait for the build", "wait.event", map[string]any{"key": "build-42"}))
	h1 := newHarness(t, harnessOpts{respond: model})
	tk := h1.submitTools("Write reports/a.md, wait for event build-42, then write reports/b.md", "workspace.write", "wait.event")
	w := h1.wait(tk.ID, task.Waiting)
	if deref(w.WaitReason) != "event:build-42" {
		t.Fatal(deref(w.WaitReason))
	}
	calls := h1.fake.CallCount()
	time.Sleep(4500 * time.Millisecond) // two supervisor ticks while idle
	if h1.fake.CallCount() != calls {
		t.Fatal("idle supervisor made model calls")
	}
	dir := h1.dir
	h1.close() // Core "restart"
	h1.stop = nil

	h2 := newHarness(t, harnessOpts{respond: model, dir: dir})
	time.Sleep(300 * time.Millisecond)
	if cur, _ := task.Get(context.Background(), h2.st.R, tk.ID); cur.State != task.Waiting {
		t.Fatalf("after restart: %s", cur.State)
	}
	ctx := context.Background()
	id1, dup1, err := h2.eng.ExternalEvent(ctx, "build-42", "ci-run-7", json.RawMessage(`{"status":"green"}`))
	_, dup2, _ := h2.eng.ExternalEvent(ctx, "build-42", "ci-run-7", json.RawMessage(`{"status":"green"}`))
	h2.eng.ExternalEvent(ctx, "build-42", "ci-run-8", nil)
	if err != nil || id1 == "" || dup1 || !dup2 {
		t.Fatalf("event intake: %v dup1=%v dup2=%v", err, dup1, dup2)
	}
	done := h2.wait(tk.ID, task.Succeeded, task.Failed)
	evs := h2.events(tk.ID)
	if done.State != task.Succeeded || countEvents(evs, "WOKE") != 1 {
		t.Fatalf("%s woke=%d %s", done.State, countEvents(evs, "WOKE"), errorDetail(evs))
	}
	if writesTo(evs, "reports/a.md") != 1 || writesTo(evs, "reports/b.md") != 1 {
		t.Fatalf("duplicate side effects: a=%d b=%d", writesTo(evs, "reports/a.md"), writesTo(evs, "reports/b.md"))
	}
	obs, _ := h2.eng.observations(ctx, done, 3)
	found := false
	for _, o := range obs {
		found = found || strings.Contains(o.Content, `"status":"green"`)
	}
	if !found {
		t.Fatal("event payload did not reach later steps")
	}
}

func TestWaitUntilAndLimits(t *testing.T) {
	soon := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	h := newHarness(t, harnessOpts{respond: waitModel(gatewaytest.Step(2, "wait", "wait.until", map[string]any{"until": soon}))})
	tk := h.submitTools("Write reports/a.md, wait a moment, then write reports/b.md", "workspace.write", "wait.until")
	h.wait(tk.ID, task.Waiting)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatal(done.State)
	}
	far := time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	h2 := newHarness(t, harnessOpts{respond: waitModel(gatewaytest.Step(2, "wait", "wait.until", map[string]any{"until": far}))})
	tk2 := h2.submitTools("Write reports/a.md, wait, then write reports/b.md", "workspace.write", "wait.until")
	if d := h2.wait(tk2.ID, task.Failed, task.Succeeded, task.Waiting); d.State != task.Failed {
		t.Fatalf("wait beyond 30 days allowed: %s", d.State)
	}
	if _, _, err := h2.eng.ExternalEvent(context.Background(), "bad key!", "", nil); err == nil {
		t.Fatal("invalid event key accepted")
	}
}

func TestPriorityOrdersScheduling(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if _, err := h.eng.SetPriority(context.Background(), "NOPE", 1); err == nil {
		t.Fatal("unknown task")
	}
	if _, err := h.eng.SetPriority(context.Background(), h.submit(rs1Objective(h.host), nil).ID, 500); err == nil {
		t.Fatal("out of range priority accepted")
	}
}
