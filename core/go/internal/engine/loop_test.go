package engine

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

func isSys(req gateway.Request, marker string) bool {
	sys := req.Messages[0].Content
	if marker == "planning module" && strings.Contains(sys, "replanning module") {
		return false
	}
	return strings.Contains(sys, marker)
}

func errorDetail(evs []task.Event) string {
	for _, e := range evs {
		if e.Type == "TASK_ERROR" {
			return string(e.Data)
		}
	}
	return ""
}

func countEvents(evs []task.Event, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// A step that fails (HTTP 404) is recovered by a bounded replan instead of
// failing the task.
func TestFailedStepIsReplanned(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tk := h.submit("Fetch http://fixture.test/missing and http://fixture.test/one, then write reports/summary.md with a summary.", nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	evs := h.events(tk.ID)
	if done.State != task.Succeeded || countEvents(evs, "REPLANNED") != 1 {
		t.Fatalf("state %s reason %s events %s\n%s", done.State, deref(done.Reason), eventTypes(evs), errorDetail(evs))
	}
	steps, _ := task.Steps(context.Background(), h.st.R, tk.ID)
	if steps[0].State != task.StepSkipped || !strings.Contains(deref(steps[0].Reason), "superseded_by_replan") {
		t.Fatalf("failed step not superseded: %s %s", steps[0].State, deref(steps[0].Reason))
	}
	if countEvents(evs, "VERIFIED") != 1 {
		t.Fatal("not verified")
	}
}

// A failed verification triggers exactly one replan, then passes.
func TestFailedVerificationReplansOnce(t *testing.T) {
	var verifies atomic.Int32
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if isSys(req, "verification module") && verifies.Add(1) == 1 {
			return gatewaytest.Reply{Content: `{"criteria":[{"criterion":"the summary file summarizes each page with its source link","met":false,"evidence_quote":""}]}`}
		}
		return goodModel(r, req)
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	evs := h.events(tk.ID)
	if done.State != task.Succeeded || countEvents(evs, "REPLANNED") != 1 || countEvents(evs, "VERIFICATION_FAILED") != 1 {
		t.Fatalf("state %s events %s", done.State, eventTypes(evs))
	}
}

// Model self-confidence is not evidence: "met" with a quote that is not in the
// output counts as a failure; repeated failures escalate to deep, then FAIL.
func TestUnsupportedClaimsFailHonestly(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if isSys(req, "verification module") {
			return gatewaytest.Reply{Content: `{"criteria":[{"criterion":"the summary file summarizes each page with its source link","met":true,"evidence_quote":"this sentence appears nowhere in the produced output at all"}]}`}
		}
		return goodModel(r, req)
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Failed, task.Succeeded)
	evs := h.events(tk.ID)
	if done.State != task.Failed || deref(done.Reason) != "verification_failed" || countEvents(evs, "REPLANNED") != maxReplans {
		t.Fatalf("state %s reason %s events %s", done.State, deref(done.Reason), eventTypes(evs))
	}
	var deep int
	h.st.R.QueryRow("select count(*) from usage_records where task_id=? and tier='deep'", tk.ID).Scan(&deep)
	if deep == 0 {
		t.Fatal("repeated verification failure did not escalate to the deep tier")
	}
}

// INT-05: an ambiguous objective gets one specific question; the task resumes
// after the owner's answer and the planner sees it.
func TestClarificationQuestion(t *testing.T) {
	var sawAnswer atomic.Bool
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if isSys(req, "planning module") {
			user := req.Messages[len(req.Messages)-1].Content
			if !strings.Contains(user, "answered") {
				return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":"Which page should I summarize?"}`}
			}
			sawAnswer.Store(strings.Contains(user, "http://fixture.test/two"))
			b, _ := json.Marshal(map[string]any{"steps": []any{
				gatewaytest.Step(1, "fetch", "web.fetch", map[string]any{"url": "http://fixture.test/two"}),
				gatewaytest.Step(2, "write", "workspace.write", map[string]any{"path": "reports/summary.md"})},
				"completion_criteria": []string{"the summary file summarizes each page with its source link"}, "question": nil})
			return gatewaytest.Reply{Content: string(b)}
		}
		return goodModel(r, req)
	}})
	tk := h.submit("Summarize the page I mean into reports/summary.md.", nil)
	waiting := h.wait(tk.ID, task.WaitingInput)
	if !strings.HasPrefix(deref(waiting.WaitReason), "input:") {
		t.Fatal(deref(waiting.WaitReason))
	}
	if _, err := h.eng.Answer(context.Background(), tk.ID, "Use http://fixture.test/two please"); err != nil {
		t.Fatal(err)
	}
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded || !sawAnswer.Load() {
		t.Fatalf("state %s, planner saw answer: %v", done.State, sawAnswer.Load())
	}
	if _, err := h.eng.Answer(context.Background(), tk.ID, "again"); err == nil {
		t.Fatal("answer accepted when no question is pending")
	}
}

// Plan review runs only for complex plans (≥ 6 steps here).
func TestPlanReviewOnlyForComplexPlans(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	simple := h.submit(rs1Objective(h.host), nil)
	h.wait(simple.ID, task.Succeeded)
	if countEvents(h.events(simple.ID), "PLAN_REVIEWED") != 0 {
		t.Fatal("simple plan was reviewed")
	}
	obj := "Fetch http://fixture.test/one http://fixture.test/two http://fixture.test/three http://fixture.test/one " +
		"http://fixture.test/two and write reports/summary.md"
	complexTask := h.submit(obj, nil)
	h.wait(complexTask.ID, task.Succeeded)
	if countEvents(h.events(complexTask.ID), "PLAN_REVIEWED") != 1 {
		t.Fatal("complex plan was not reviewed")
	}
}

func TestQuoteFound(t *testing.T) {
	ev := []evidenceItem{{Content: "## Source 1\nA short **factual** summary of the page.\n\nSource: http://x"}}
	if !quoteFound("a short factual summary of the page", ev) || !quoteFound("short factual summary of page", ev) {
		t.Fatal("formatting differences should not matter")
	}
	if quoteFound("completely different words here", ev) || quoteFound("page", ev) {
		t.Fatal("unrelated or trivial quote accepted")
	}
}
