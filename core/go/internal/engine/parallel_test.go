package engine

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

// Blueprint P6 parallel subagents (ADR-0047): with two workers, consecutive
// delegations run at the same time; the parent waits for both, merges their
// findings, and the tree stays within its budget.
func TestParallelSubagents(t *testing.T) {
	var planning, peak atomic.Int32
	both := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var merge string
	model := func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		user := req.Messages[len(req.Messages)-1].Content
		switch {
		case isSys(req, "planning module") && strings.Contains(user, "[Subagent briefing"):
			n := planning.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			if n == 2 {
				once.Do(func() { close(both) })
			}
			select { // the first child waits until the second is planning too (proves overlap)
			case <-both:
			case <-time.After(10 * time.Second):
			}
			planning.Add(-1)
			u := briefURL.FindStringSubmatch(user)
			return planReply(
				gatewaytest.Step(1, "fetch "+u[1], "web.fetch", map[string]any{"url": u[1]}),
				gatewaytest.Step(2, "write findings", "workspace.write", map[string]any{"path": "findings.md"}))
		case isSys(req, "writing module") && strings.Contains(user, "reports/compare.md"):
			mu.Lock()
			merge = user
			mu.Unlock()
			return goodModel(r, req)
		case isSys(req, "planning module"):
			return planReply(
				gatewaytest.Step(1, "delegate one", "agent.delegate", map[string]any{"brief": "Summarize http://fixture.test/one"}),
				gatewaytest.Step(2, "delegate two", "agent.delegate", map[string]any{"brief": "Summarize http://fixture.test/two"}),
				gatewaytest.Step(3, "merge", "workspace.write", map[string]any{"path": "reports/compare.md"}))
		}
		return goodModel(r, req)
	}
	h := newHarness(t, harnessOpts{respond: model, workers: 3})
	tk := h.submitTools("Compare http://fixture.test/one and http://fixture.test/two using two subagents; write reports/compare.md",
		"web.fetch", "workspace.write", "agent.delegate")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s", done.State, errorDetail(h.events(tk.ID)))
	}
	if peak.Load() < 2 {
		t.Fatalf("children did not run concurrently (peak %d)", peak.Load())
	}
	mu.Lock()
	m := merge
	mu.Unlock()
	if !strings.Contains(m, "fixture.test/one") || !strings.Contains(m, "fixture.test/two") {
		t.Fatalf("the merge did not see both children's findings")
	}
	// Budget accuracy across the tree: everything spent belongs to tasks of this tree and stays under its cap.
	spent, err := h.eng.gov.TreeSpent(context.Background(), tk.ID)
	if err != nil || spent > done.Budget.MaxCostUSD {
		t.Fatalf("tree spent %v of %v (%v)", spent, done.Budget.MaxCostUSD, err)
	}
	var findings int
	h.st.R.QueryRow("select count(*) from shared_context where root_task_id=? and kind='finding'", tk.ID).Scan(&findings)
	if findings != 2 {
		t.Fatalf("shared task context findings: %d", findings)
	}
}
