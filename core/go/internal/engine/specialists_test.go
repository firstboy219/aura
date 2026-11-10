package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

// Blueprint P6 specialist subagent profiles (ADR-0046): a delegated child gets
// its profile's instructions and tier, and only tools in both its profile and
// its parent's grant.
func TestSpecialistProfiles(t *testing.T) {
	var mu sync.Mutex
	var childPrompt, plannerDocs string
	profile := "researcher"
	model := func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		user := req.Messages[len(req.Messages)-1].Content
		switch {
		case isSys(req, "replanning module"):
			return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":null}`}
		case isSys(req, "planning module") && strings.Contains(user, "[Subagent briefing"):
			mu.Lock()
			childPrompt = user
			mu.Unlock()
			return planReply(
				gatewaytest.Step(1, "fetch", "web.fetch", map[string]any{"url": "http://fixture.test/one"}),
				gatewaytest.Step(2, "write findings", "workspace.write", map[string]any{"path": "findings.md"}))
		case isSys(req, "planning module"):
			mu.Lock()
			plannerDocs = req.Messages[0].Content
			mu.Unlock()
			return planReply(
				gatewaytest.Step(1, "ask the researcher", "agent.delegate", map[string]any{"brief": "Summarize http://fixture.test/one",
					"input": `{"profile": "` + profile + `"}`}),
				gatewaytest.Step(2, "merge", "workspace.write", map[string]any{"path": "reports/r.md"}))
		}
		return goodModel(r, req)
	}
	h := newHarness(t, harnessOpts{respond: model})
	h.eng.cfg.Specialists = []config.SpecialistProfile{{Name: "researcher", Description: "finds and cites primary sources",
		Tools: []string{"web.fetch", "web.search", "workspace.write"}, Tier: "deep", Instructions: "Always quote the source sentence."}}
	ctx := context.Background()
	tk := h.submitTools("Use the researcher to summarize http://fixture.test/one into reports/r.md", "web.fetch", "workspace.write", "agent.delegate")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s", done.State, errorDetail(h.events(tk.ID)))
	}
	mu.Lock()
	docs, cp := plannerDocs, childPrompt
	mu.Unlock() // the model callback takes the same lock
	if !strings.Contains(docs, "researcher — finds and cites primary sources") {
		t.Fatal("the planner was not told about the specialist")
	}
	if !strings.Contains(cp, "researcher specialist") || !strings.Contains(cp, "Always quote the source sentence.") {
		t.Fatalf("child did not get its profile: %s", cp)
	}
	rows, _ := h.st.R.Query("select id from tasks where parent_task_id=?", tk.ID)
	var childID string
	for rows.Next() {
		rows.Scan(&childID)
	}
	rows.Close()
	child, err := task.Get(ctx, h.st.R, childID)
	if err != nil {
		t.Fatal(err)
	}
	// web.search is in the profile but not in the parent's grant: not given.
	if strings.Join(child.Scope.Tools, ",") != "web.fetch,workspace.write" || child.TierOverride == nil || *child.TierOverride != "deep" {
		t.Fatalf("child tools %v tier %v", child.Scope.Tools, child.TierOverride)
	}
	// An unknown profile is refused.
	profile = "nobody"
	tk2 := h.submitTools("Use the researcher to summarize http://fixture.test/one into reports/r.md", "web.fetch", "workspace.write", "agent.delegate")
	h.wait(tk2.ID, task.Failed, task.Succeeded)
	if !strings.Contains(strings.Join(deniedReasons(t, h, tk2.ID), ","), "unknown_specialist") {
		t.Fatalf("unknown profile: %v", deniedReasons(t, h, tk2.ID))
	}
}
