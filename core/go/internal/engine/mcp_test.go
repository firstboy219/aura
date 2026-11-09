package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/mcp"
	"aura.local/core/internal/mcp/mcptest"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

func init() {
	// Owner-declared tools (as app.buildMCP would register from config). The
	// server's own description of "lookup" asks to be A0 — irrelevant.
	policy.RegisterMCPTool("mcp.fake.lookup", "read", policy.A0)
	policy.RegisterMCPTool("mcp.fake.send", "write_external", policy.A2)
}

func fakeMCP(t *testing.T, logPath string, slow bool) map[string]*mcp.Client {
	env := []string{"PATH=" + os.Getenv("PATH"), "MCP_LOG=" + logPath}
	if slow {
		env = append(env, "MCP_SLOW=1")
	}
	c := mcp.New("fake", []string{"python3", "-I", "-c", mcptest.Script}, env, 120*time.Second, nil)
	t.Cleanup(c.Close)
	return map[string]*mcp.Client{"fake": c}
}

func mcpModel(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
	if isSys(req, "planning module") {
		return planReply(
			gatewaytest.Step(1, "look up record 7", "mcp.fake.lookup", map[string]any{"input": `{"id": 7}`}),
			gatewaytest.Step(2, "send the note", "mcp.fake.send", map[string]any{"input": `{"to": "ops", "text": "record 7 checked"}`}))
	}
	return goodModel(r, req)
}

func mcpCalls(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "\n")
}

func TestMCPToolsUnderCorePolicy(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	h := newHarness(t, harnessOpts{respond: mcpModel, mcp: fakeMCP(t, logPath, false),
		toolDocs: map[string]string{"mcp.fake.lookup": "Look up a record by id.", "mcp.fake.send": "Send a note."}})
	tk := h.submitTools("Look up record 7 and send a note to ops", "mcp.fake.lookup", "mcp.fake.send")
	h.wait(tk.ID, task.WaitingApproval)
	if mcpCalls(logPath) != 1 {
		t.Fatalf("A2 tool ran before approval (calls=%d)", mcpCalls(logPath))
	}
	aps, _ := policy.Pending(context.Background(), h.st.R)
	if len(aps) != 1 || aps[0].ToolID != "mcp.fake.send" || aps[0].Parameters["to"] != "ops" {
		t.Fatalf("%+v", aps)
	}
	h.eng.Approve(context.Background(), aps[0].ID, aps[0].ActionHash, "owner")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded || mcpCalls(logPath) != 2 {
		t.Fatalf("%s calls=%d %s", done.State, mcpCalls(logPath), errorDetail(h.events(tk.ID)))
	}
	obs, _ := h.eng.observations(context.Background(), done, 99)
	if len(obs) < 1 || obs[0].Content != "record for 7" || obs[0].Trust != "untrusted" {
		t.Fatalf("%+v", obs)
	}
	// A tool the server offers but the owner did not declare cannot be granted.
	if _, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: "x", Tools: []string{"mcp.fake.undeclared"}}); err == nil {
		t.Fatal("undeclared MCP tool accepted")
	}
}

// A Core crash during an external write: the outcome is unknown, so the call is
// not repeated automatically; the owner decides through an approval.
func TestMCPOutcomeUnknownAfterCrash(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "calls.log")
	model := func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if isSys(req, "planning module") {
			return planReply(gatewaytest.Step(1, "send", "mcp.fake.send", map[string]any{"input": `{"to": "ops"}`}))
		}
		return goodModel(r, req)
	}
	h1 := newHarness(t, harnessOpts{respond: model, mcp: fakeMCP(t, logPath, true)})
	tk := h1.submitTools("Send a note to ops", "mcp.fake.send")
	h1.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(context.Background(), h1.st.R)
	h1.eng.Approve(context.Background(), aps[0].ID, aps[0].ActionHash, "owner")
	deadline := time.Now().Add(10 * time.Second)
	for mcpCalls(logPath) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	dir := h1.dir
	h1.close() // the Core dies while the external call is in flight
	h1.stop = nil

	h2 := newHarness(t, harnessOpts{respond: model, mcp: fakeMCP(t, logPath, false), dir: dir})
	w := h2.wait(tk.ID, task.WaitingApproval)
	if deref(w.Reason) != "OUTCOME_UNKNOWN" || mcpCalls(logPath) != 1 {
		t.Fatalf("reason %s calls=%d", deref(w.Reason), mcpCalls(logPath))
	}
	time.Sleep(2500 * time.Millisecond)
	if mcpCalls(logPath) != 1 {
		t.Fatal("external write repeated automatically")
	}
	aps, _ = policy.Pending(context.Background(), h2.st.R)
	if len(aps) != 1 {
		t.Fatalf("approvals %d", len(aps))
	}
	h2.eng.Approve(context.Background(), aps[0].ID, aps[0].ActionHash, "owner") // owner decides to send again
	done := h2.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded || mcpCalls(logPath) != 2 {
		t.Fatalf("%s calls=%d", done.State, mcpCalls(logPath))
	}
}
