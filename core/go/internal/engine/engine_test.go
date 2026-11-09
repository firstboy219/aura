package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/obs"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

func countWrites(evs []task.Event) (performed, skipped int) {
	for _, e := range evs {
		if e.Type != "TOOL_SUCCEEDED" {
			continue
		}
		var d map[string]any
		json.Unmarshal(e.Data, &d)
		if d["tool_id"] != "workspace.write" {
			continue
		}
		if d["skipped"] == true {
			skipped++
		} else {
			performed++
		}
	}
	return
}

// RS-1 with the fake provider and the real Python worker.
func TestRS1FakeProvider(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed, task.BudgetExceeded)
	if done.State != task.Succeeded {
		t.Fatalf("state %s reason %s", done.State, deref(done.Reason))
	}
	b, err := os.ReadFile(filepath.Join(h.cfg.Workspace.Root, tk.ID, "reports", "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(string(b), "## Source")[1:]
	if len(sections) != 3 {
		t.Fatalf("want 3 sections:\n%s", b)
	}
	for i, s := range sections {
		if words := len(strings.Fields(s)); words > 150 {
			t.Errorf("section %d has %d words", i+1, words)
		}
		if !regexp.MustCompile(`Source: http://fixture\.test/(one|two|three)`).MatchString(s) {
			t.Errorf("section %d lacks its source link", i+1)
		}
	}
	evs := h.events(tk.ID)
	if p, _ := countWrites(evs); p != 1 {
		t.Fatalf("performed %d writes, want exactly 1", p)
	}
	for _, e := range evs {
		if e.Type == "CAPABILITY_DENIED" {
			t.Fatalf("unexpected denial: %s", e.Data)
		}
	}
	if done.CostUSD <= 0 || done.CostReservedUSD > 1e-9 {
		t.Fatalf("cost accounting: actual=%v reserved=%v", done.CostUSD, done.CostReservedUSD)
	}
	// The injected instruction in page 3 changed nothing: no file outside the
	// summary, no extra fetch, grants revoked at the end.
	var fetches int
	for _, e := range evs {
		if e.Type == "TOOL_STARTED" {
			fetches++
		}
	}
	if fetches != 3 {
		t.Fatalf("fetches %d", fetches)
	}
	waitAnyEvent(t, h, tk.ID, "REFLECTED", "REFLECTION_SKIPPED") // reflection runs after success
	var live int
	h.st.R.QueryRow("select count(*) from capability_grants where task_id=? and revoked_at is null", tk.ID).Scan(&live)
	if live != 0 {
		t.Fatal("grants not revoked after completion")
	}
}

func TestApprovalFlow(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	obj := strings.Replace(rs1Objective(h.host), "reports/summary.md", "published/report.md", 1)
	tk := h.submit(obj, nil)
	h.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(context.Background(), h.st.R)
	if len(aps) != 1 || aps[0].Target != "published/report.md" || aps[0].Parameters["content"] == nil {
		t.Fatalf("%+v", aps)
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Workspace.Root, tk.ID, "published", "report.md")); err == nil {
		t.Fatal("file written before approval")
	}
	if _, err := h.eng.Approve(context.Background(), aps[0].ID, strings.Repeat("0", 64), "owner"); err == nil {
		t.Fatal("approved with a wrong hash")
	}
	if _, err := h.eng.Approve(context.Background(), aps[0].ID, aps[0].ActionHash, "owner"); err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	b, _ := os.ReadFile(filepath.Join(h.cfg.Workspace.Root, tk.ID, "published", "report.md"))
	if string(b) != aps[0].Parameters["content"] {
		t.Fatal("written content differs from the approved content")
	}
	got, _ := policy.GetApproval(context.Background(), h.st.R, aps[0].ID)
	if got.Status != "consumed" {
		t.Fatalf("approval %s", got.Status)
	}
}

func TestRejectFailsTask(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	obj := strings.Replace(rs1Objective(h.host), "reports/summary.md", "published/report.md", 1)
	tk := h.submit(obj, nil)
	h.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(context.Background(), h.st.R)
	h.eng.Reject(context.Background(), aps[0].ID, "owner")
	done := h.wait(tk.ID, task.Failed)
	if deref(done.Reason) != "approval_rejected" {
		t.Fatal(deref(done.Reason))
	}
}

func TestBudgetExceeded(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tk := h.submit(rs1Objective(h.host), &task.Budget{MaxCostUSD: 0.000001})
	done := h.wait(tk.ID, task.BudgetExceeded, task.Failed, task.Succeeded)
	if done.State != task.BudgetExceeded || h.fake.CallCount() != 0 {
		t.Fatalf("state %s calls %d", done.State, h.fake.CallCount())
	}
	tk2 := h.submit(rs1Objective(h.host), &task.Budget{MaxIterations: 1})
	done2 := h.wait(tk2.ID, task.BudgetExceeded, task.Failed, task.Succeeded)
	if done2.State != task.BudgetExceeded || deref(done2.Reason) != "budget_exceeded_iterations" {
		t.Fatalf("iterations: %s %s", done2.State, deref(done2.Reason))
	}
}

func TestInvalidModelOutputFailsTask(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
		return gatewaytest.Reply{Content: `{"steps": "not a list"}`}
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Failed)
	if deref(done.Reason) != "invalid_model_output" || h.fake.CallCount() != 2 {
		t.Fatalf("reason %s calls %d (want exactly one repair)", deref(done.Reason), h.fake.CallCount())
	}
	steps, _ := task.Steps(context.Background(), h.st.R, tk.ID)
	if len(steps) != 0 {
		t.Fatal("invalid output became steps")
	}
}

// Kill switch: cancels the in-flight provider call, stops the worker, blocks
// new calls and pauses the task with recoverable state.
func TestKillSwitch(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		rep := goodModel(r, req)
		rep.Delay = 20 * time.Second
		return rep
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	deadline := time.Now().Add(10 * time.Second)
	for h.fake.CallCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now()
	n, err := h.eng.Stop(context.Background(), "owner")
	if err != nil || n != 1 {
		t.Fatalf("%v %d", err, n)
	}
	elapsed := time.Since(start)
	done := h.wait(tk.ID, task.Paused)
	if deref(done.WaitReason) != "kill_switch" {
		t.Fatal(deref(done.WaitReason))
	}
	if st, _, _ := h.sup.State(); st != "stopped" {
		t.Fatalf("worker %s", st)
	}
	var outcome string
	h.st.R.QueryRow("select outcome from usage_records where task_id=?", tk.ID).Scan(&outcome)
	if outcome != "cancelled" {
		t.Fatalf("provider call outcome %q", outcome)
	}
	if _, err := h.eng.Resume(context.Background(), tk.ID); err == nil {
		t.Fatal("resume allowed while halted")
	}
	t.Logf("kill switch latency: %v", elapsed)
	if elapsed > 5*time.Second {
		t.Fatalf("kill switch took %v", elapsed)
	}
}

func TestPauseResumeCancel(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if strings.Contains(req.Messages[0].Content, "writing module") {
			<-gate
		}
		return goodModel(r, req)
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(eventTypes(h.events(tk.ID)), "STEP_STARTED") && h.fake.CallCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := h.eng.Pause(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Paused)
	close(gate)
	if _, err := h.eng.Resume(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	if p, _ := countWrites(h.events(tk.ID)); p != 1 {
		t.Fatalf("writes %d", p)
	}

	tk2 := h.submit(rs1Objective(h.host), nil)
	if _, err := h.eng.Cancel(context.Background(), tk2.ID); err != nil {
		t.Fatal(err)
	}
	h.wait(tk2.ID, task.Cancelled)
	if _, err := h.eng.Resume(context.Background(), tk2.ID); err == nil {
		t.Fatal("cancelled task resumed")
	}
}

func eventTypes(evs []task.Event) string {
	var s []string
	for _, e := range evs {
		s = append(s, e.Type)
	}
	return strings.Join(s, ",")
}

// Secret scan (D.1 P1): the provider key never appears in events, audit,
// checkpoints, or usage records.
func TestSecretScan(t *testing.T) {
	const key = "sk-or-v1-secretscan-0123456789abcdef"
	obs.RegisterSecret(key)
	h := newHarness(t, harnessOpts{})
	tk := h.submit(rs1Objective(h.host)+" Note: "+key, nil)
	h.wait(tk.ID, task.Succeeded, task.Failed)
	for _, q := range []string{"select group_concat(data_json) from task_events", "select group_concat(data_json) from audit_events",
		"select group_concat(objective) from tasks where 0", "select group_concat(coalesce(request_id,'')) from usage_records"} {
		var s *string
		h.st.R.QueryRow(q).Scan(&s)
		if s != nil && strings.Contains(*s, key) {
			t.Fatalf("key leaked via %q", q)
		}
	}
}

func TestSubmitValidation(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	bad := []CreateInput{
		{Objective: ""},
		{Objective: "x", Tools: []string{"shell.exec"}},
		{Objective: "x", AllowDomains: []string{"127.0.0.1"}},
		{Objective: "x", AllowDomains: []string{"localhost"}},
		{Objective: "x", Budget: task.Budget{MaxCostUSD: 100}},
	}
	for _, in := range bad {
		if _, _, err := h.eng.Submit(context.Background(), in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	in := CreateInput{Objective: "x", AllowDomains: []string{"example.com"}, IdempotencyKey: "k1"}
	a, created1, _ := h.eng.Submit(context.Background(), in)
	b, created2, _ := h.eng.Submit(context.Background(), in)
	if a.ID != b.ID || !created1 || created2 {
		t.Fatal("idempotency key did not deduplicate")
	}
	h.eng.Cancel(context.Background(), a.ID)
}

func TestExpectedOutputs(t *testing.T) {
	got := strings.Join(ExpectedOutputs("Fetch https://www.rfc-editor.org/rfc/rfc2606.txt and http://a.test/x.md, then write "+
		"reports/summary.md and notes.txt (also \"data/out.json\"). Again reports/summary.md."), ",")
	if got != "reports/summary.md,notes.txt,data/out.json" {
		t.Fatal(got)
	}
}

// Regression (found by the first free-model RS-1 run): a plan that skips the
// write the objective asks for must not end as SUCCEEDED.
func TestPlanMissingRequiredOutputFails(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		rep := goodModel(r, req)
		if strings.Contains(req.Messages[0].Content, "planning module") {
			var p map[string]any
			json.Unmarshal([]byte(rep.Content), &p)
			steps := p["steps"].([]any)
			p["steps"] = steps[:len(steps)-1] // drop the write step
			b, _ := json.Marshal(p)
			rep.Content = string(b)
		}
		return rep
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Failed, task.Succeeded)
	if done.State != task.Failed || deref(done.Reason) != "invalid_plan" {
		t.Fatalf("state %s reason %s", done.State, deref(done.Reason))
	}
	if h.fake.CallCount() != 2 {
		t.Fatalf("want one plan repair (2 calls), got %d", h.fake.CallCount())
	}
}

func TestPurgeContentKeepsEventsAndResults(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tk := h.submit(rs1Objective(h.host), nil)
	h.wait(tk.ID, task.Succeeded)
	running := h.submit(rs1Objective(h.host), nil) // not finished yet: must not be purged
	h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("update checkpoints set purge_after = 1")
		return err
	})
	n, err := h.eng.PurgeContent(context.Background())
	if err != nil || n == 0 {
		t.Fatalf("%d %v", n, err)
	}
	var left int
	h.st.R.QueryRow("select count(*) from checkpoints where task_id=?", tk.ID).Scan(&left)
	if left != 0 {
		t.Fatal("expired checkpoints not purged")
	}
	if !strings.Contains(eventTypes(h.events(tk.ID)), "CONTENT_PURGED") {
		t.Fatal("purge not recorded")
	}
	if _, err := os.Stat(filepath.Join(h.cfg.Workspace.Root, tk.ID, "reports", "summary.md")); err != nil {
		t.Fatal("owner result was deleted")
	}
	h.eng.Cancel(context.Background(), running.ID)
}
