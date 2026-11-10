package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/task"
)

// askingPlanner always asks one question until the owner context says it was
// answered or left unanswered; then it plans normally.
func askingPlanner(sawTimeout *atomic.Bool) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if isSys(req, "planning module") {
			user := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(user, "did not answer") {
				return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":"Which page should I summarize?"}`}
			}
			sawTimeout.Store(true)
			b, _ := json.Marshal(map[string]any{"steps": []any{
				gatewaytest.Step(1, "fetch", "web.fetch", map[string]any{"url": "http://fixture.test/two"}),
				gatewaytest.Step(2, "write", "workspace.write", map[string]any{"path": "reports/summary.md"})},
				"completion_criteria": []string{"the summary file summarizes each page with its source link"}, "question": nil})
			return gatewaytest.Reply{Content: string(b)}
		}
		return goodModel(r, req)
	}
}

func backdateInput(t *testing.T, h *harness, taskID string, seconds int) {
	t.Helper()
	ctx := context.Background()
	if err := h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "update task_inputs set created_at=? where task_id=? and status='pending'", ids.NowMs()-int64(seconds)*1000, taskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func countRows(h *harness, q string, args ...any) int {
	var n int
	h.st.R.QueryRow(q, args...).Scan(&n)
	return n
}

// §8.2 INPUT_TIMEOUT: one reminder, then FAILED with the canonical code.
func TestUnansweredQuestionRemindsThenTimesOut(t *testing.T) {
	var saw atomic.Bool
	h := newHarness(t, harnessOpts{respond: askingPlanner(&saw)})
	ctx := context.Background()
	tk := h.submit("Summarize the page I mean into reports/summary.md.", nil)
	h.wait(tk.ID, task.WaitingInput)
	pol := h.eng.cfg.Inputs

	h.eng.ExpireInputs(ctx) // too early: nothing happens
	backdateInput(t, h, tk.ID, pol.RemindAfterSeconds+1)
	h.eng.ExpireInputs(ctx)
	h.eng.ExpireInputs(ctx) // the reminder is sent once
	if n := countRows(h, "select count(*) from notifications where task_id=? and kind='info'", tk.ID); n != 1 || countEvents(h.events(tk.ID), "INPUT_REMINDED") != 1 {
		t.Fatalf("reminders: %d notifications, events %s", n, eventTypes(h.events(tk.ID)))
	}
	if cur, _ := task.Get(ctx, h.st.R, tk.ID); cur.State != task.WaitingInput {
		t.Fatalf("a reminder must not change the state: %s", cur.State)
	}

	backdateInput(t, h, tk.ID, pol.TimeoutSeconds+1)
	h.eng.ExpireInputs(ctx)
	done := h.wait(tk.ID, task.Failed)
	if deref(done.ReasonCode) != "INPUT_TIMEOUT" || countEvents(h.events(tk.ID), "INPUT_TIMEOUT") != 1 {
		t.Fatalf("reason code %q", deref(done.ReasonCode))
	}
	if n := countRows(h, "select count(*) from task_inputs where task_id=? and status='timed_out'", tk.ID); n != 1 {
		t.Fatal("question not marked timed_out")
	}
	if _, err := h.eng.Answer(ctx, tk.ID, "too late"); err == nil {
		t.Fatal("a late answer was accepted on a failed task")
	}
	if saw.Load() {
		t.Fatal("planner ran after a fail-on-timeout")
	}
}

// on_timeout=continue resumes planning, tells the planner the owner did not
// answer, and does not let it ask again.
func TestUnansweredQuestionContinuesWithStatedAssumptions(t *testing.T) {
	var saw atomic.Bool
	h := newHarness(t, harnessOpts{respond: askingPlanner(&saw)})
	h.eng.cfg.Inputs.OnTimeout = "continue"
	ctx := context.Background()
	tk := h.submit("Summarize the page I mean into reports/summary.md.", nil)
	h.wait(tk.ID, task.WaitingInput)
	backdateInput(t, h, tk.ID, h.eng.cfg.Inputs.TimeoutSeconds+1)
	h.eng.ExpireInputs(ctx)
	done := h.wait(tk.ID, task.Succeeded, task.Failed, task.WaitingInput)
	if done.State != task.Succeeded || !saw.Load() {
		t.Fatalf("state %s, planner told about the timeout: %v; events %s", done.State, saw.Load(), eventTypes(h.events(tk.ID)))
	}
	if n := countRows(h, "select count(*) from task_inputs where task_id=?", tk.ID); n != 1 {
		t.Fatalf("asked again after a timeout: %d questions", n)
	}
}

func TestInputPolicyValidation(t *testing.T) {
	for _, y := range []string{"inputs: {on_timeout: maybe}", "inputs: {timeout_seconds: 60, remind_after_seconds: 60}", "inputs: {timeout_seconds: 0}",
		"prompt_budget_chars: {fast: 100}", "prompt_budget_chars: {turbo: 50000}"} {
		if _, err := config.Parse([]byte("config_version: 1\n"+y+"\n"), func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %q", y)
		}
	}
}
