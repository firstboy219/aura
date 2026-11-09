package engine

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

var briefURL = regexp.MustCompile(`Summarize (http://fixture\.test/[a-z]+)`)

type rs7Model struct {
	mu         sync.Mutex
	prompts    []string // child planning prompts
	writes     []string // writing prompts (all tasks)
	gate       chan struct{}
	childTools []string
}

func (m *rs7Model) respond(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
	user := req.Messages[len(req.Messages)-1].Content
	switch {
	case isSys(req, "planning module") && strings.Contains(user, "[Subagent briefing"):
		m.mu.Lock()
		m.prompts = append(m.prompts, req.Messages[0].Content+"\n"+user)
		gate := m.gate
		m.mu.Unlock()
		if gate != nil && len(m.prompts) == 2 {
			<-gate // second child plans only after the owner's correction
		}
		u := briefURL.FindStringSubmatch(user)
		return planReply(
			gatewaytest.Step(1, "fetch "+u[1], "web.fetch", map[string]any{"url": u[1]}),
			gatewaytest.Step(2, "write findings", "workspace.write", map[string]any{"path": "findings.md"}))
	case isSys(req, "writing module"):
		m.mu.Lock()
		m.writes = append(m.writes, user)
		m.mu.Unlock()
		return goodModel(r, req)
	case isSys(req, "planning module"):
		return planReply(
			gatewaytest.Step(1, "delegate one", "agent.delegate", map[string]any{"brief": "Summarize http://fixture.test/one", "tools": m.childTools}),
			gatewaytest.Step(2, "delegate two", "agent.delegate", map[string]any{"brief": "Summarize http://fixture.test/two", "tools": m.childTools}),
			gatewaytest.Step(3, "merge", "workspace.write", map[string]any{"path": "reports/compare.md"}))
	}
	return goodModel(r, req)
}

// RS-7: two sequential subagents, an owner correction that reaches the active
// subagent, findings merged by the main task, one tree with rolled-up cost.
func TestRS7Subagents(t *testing.T) {
	m := &rs7Model{gate: make(chan struct{}), childTools: []string{"web.fetch", "workspace.write"}}
	h := newHarness(t, harnessOpts{respond: m.respond})
	root := h.submitTools("Compare the pages using subagents and write reports/compare.md", "web.fetch", "workspace.write", "agent.delegate")
	// Wait until the second child is planning, then correct the plan.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.prompts)
		m.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := h.eng.AddContext(context.Background(), root.ID, "Also add a price comparison."); err != nil {
		t.Fatal(err)
	}
	close(m.gate)
	done := h.wait(root.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s %s", done.State, deref(done.Reason), errorDetail(h.events(root.ID)))
	}
	tree, err := h.eng.Tree(context.Background(), root.ID)
	if err != nil || len(tree.Children) != 2 {
		t.Fatalf("tree: %v %+v", err, tree)
	}
	sum := tree.Task.CostUSD
	for _, c := range tree.Children {
		if c.Task.Depth != 1 || c.Task.AgentRole != "specialist" || c.Task.State != task.Succeeded || *c.Task.ParentTaskID != root.ID {
			t.Fatalf("child %+v", c.Task)
		}
		if strings.Join(c.Task.Scope.Tools, ",") != "web.fetch,workspace.write" {
			t.Fatalf("child tools %v", c.Task.Scope.Tools)
		}
		sum += c.TreeCostUSD
	}
	if tree.TreeCostUSD < sum-1e-12 || tree.TreeCostUSD <= tree.Task.CostUSD {
		t.Fatalf("cost not rolled up: %v vs %v", tree.TreeCostUSD, sum)
	}
	// The correction reached the second (active) subagent; children may not ask the owner.
	m.mu.Lock()
	writes := append([]string(nil), m.writes...)
	m.mu.Unlock()
	// writes: child one (before the correction), child two, main merge (after it).
	if len(writes) != 3 || strings.Contains(writes[0], "price comparison") ||
		!strings.Contains(writes[1], "price comparison") || !strings.Contains(writes[2], "price comparison") {
		t.Fatalf("owner correction did not reach the active subagent and the main task: %d writes", len(writes))
	}
	if !strings.Contains(writes[2], "Subagent ") {
		t.Fatal("main task did not receive the subagents' findings")
	}
	if !strings.Contains(m.prompts[0], `Set "question" to null`) {
		t.Fatal("a subagent was allowed to ask the owner")
	}
	var findings, untrusted int
	h.st.R.QueryRow("select count(*), sum(trust='untrusted') from shared_context where root_task_id=? and kind='finding'", root.ID).Scan(&findings, &untrusted)
	if findings != 2 || untrusted != 2 {
		t.Fatalf("findings %d untrusted %d", findings, untrusted)
	}
	evs := h.events(root.ID)
	if countEvents(evs, "SUBAGENT_STARTED") != 2 || countEvents(evs, "WOKE") != 2 {
		t.Fatalf("events %s", eventTypes(evs))
	}
}

// A subagent cannot get tools its parent does not have (audited denial).
func TestDelegationGrantWideningDenied(t *testing.T) {
	m := &rs7Model{childTools: []string{"web.fetch", "web.search"}}
	h := newHarness(t, harnessOpts{respond: m.respond})
	root := h.submitTools("Compare the pages using subagents and write reports/compare.md", "web.fetch", "workspace.write", "agent.delegate")
	done := h.wait(root.ID, task.Failed, task.Succeeded)
	if done.State != task.Failed || !strings.Contains(strings.Join(deniedReasons(t, h, root.ID), ","), "grant_widening") {
		t.Fatalf("%s %v", done.State, deniedReasons(t, h, root.ID))
	}
	kids, _ := task.Children(context.Background(), h.st.R, root.ID)
	if len(kids) != 0 {
		t.Fatal("child created despite widening")
	}
}

// Cancelling the root cancels every descendant.
func TestCancelRootCancelsDescendants(t *testing.T) {
	block := make(chan struct{})
	m := &rs7Model{childTools: []string{"web.fetch", "workspace.write"}}
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if strings.Contains(req.Messages[len(req.Messages)-1].Content, "[Subagent briefing") {
			<-block
		}
		return m.respond(r, req)
	}})
	defer close(block)
	root := h.submitTools("Compare the pages using subagents and write reports/compare.md", "web.fetch", "workspace.write", "agent.delegate")
	h.wait(root.ID, task.Waiting)
	if _, err := h.eng.Cancel(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	kids, _ := task.Children(context.Background(), h.st.R, root.ID)
	if len(kids) != 1 {
		t.Fatalf("children %d", len(kids))
	}
	h.wait(kids[0].ID, task.Cancelled)
}

// Tree budget: a child cannot overspend the root's budget.
func TestTreeBudgetBoundsChildren(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	root := &task.Task{Objective: "root", Budget: task.Budget{MaxCostUSD: 0.10, MaxIterations: 20, MaxWallSeconds: 60}, ConfigHash: "h"}
	h.st.Write(ctx, func(tx *sql.Tx) error { return task.Insert(ctx, tx, root) })
	rid := root.ID
	child := &task.Task{Objective: "child", ParentTaskID: &rid, RootTaskID: rid, Depth: 1, Budget: task.Budget{MaxCostUSD: 1, MaxIterations: 20, MaxWallSeconds: 60}, ConfigHash: "h"}
	h.st.Write(ctx, func(tx *sql.Tx) error { return task.Insert(ctx, tx, child) })
	call := func(id string, est float64) error {
		_, err := h.eng.gov.Reserve(ctx, budgetCall(id, rid, est))
		return err
	}
	if err := call(child.ID, 0.08); err != nil {
		t.Fatal(err)
	}
	if err := call(child.ID, 0.05); err == nil {
		t.Fatal("child spent beyond the root budget")
	}
	if err := call(rid, 0.05); err == nil {
		t.Fatal("root spent beyond its tree budget")
	}
}

func budgetCall(taskID, rootID string, est float64) budget.Call {
	return budget.Call{TaskID: taskID, Tier: "standard", ProviderID: "x", Route: "r", APIModelID: "m", EstimateUSD: est,
		TaskLimitUSD: 1, RootTaskID: rootID, RootLimitUSD: 0.10}
}
