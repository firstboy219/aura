package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/sandbox"
	"aura.local/core/internal/task"
)

var (
	sbxOnce sync.Once
	sbxRT   *sandbox.Runtime
	sbxErr  error
)

// testSandbox shares one compiled CPython module between tests.
func testSandbox(t *testing.T, want bool) *sandbox.Runtime {
	if !want {
		return nil
	}
	p := os.Getenv("AURA_PYTHON_WASM")
	if p == "" {
		p, _ = filepath.Abs("../../../../.cache/python-3.12.0.wasm")
	}
	if _, err := os.Stat(p); err != nil {
		t.Skip("python WASM module not present; run deploy/fetch-python-wasm.sh")
	}
	sbxOnce.Do(func() {
		sbxRT, sbxErr = sandbox.NewPython(context.Background(), p, filepath.Join(os.TempDir(), "aura-wasm-cache"), sandbox.Limits{Timeout: 20 * time.Second})
	})
	if sbxErr != nil {
		t.Fatal(sbxErr)
	}
	return sbxRT
}

const csvStatsSource = `import csv, io, statistics

def run(input):
    rows = list(csv.DictReader(io.StringIO(input["file_text"])))
    col = input.get("column", "amount")
    values = [float(r[col]) for r in rows]
    return {"rows": len(rows), "column": col, "sum": round(sum(values), 6), "mean": round(statistics.fmean(values), 6)}
`

func csvDraft(source string) string {
	tests := []map[string]string{
		{"input_json": `{"file_text": "item,amount\na,1\nb,2\nc,6\n", "column": "amount"}`, "expected_json": `{"rows": 3, "column": "amount", "sum": 9.0, "mean": 3.0}`},
		{"input_json": `{"file_text": "item,amount\nx,10\n", "column": "amount"}`, "expected_json": `{"rows": 1, "column": "amount", "sum": 10.0, "mean": 10.0}`},
		{"input_json": `{"file_text": "item,amount\np,0.5\nq,1.5\n"}`, "expected_json": `{"rows": 2, "column": "amount", "sum": 2.0, "mean": 1.0}`},
	}
	b, _ := json.Marshal(map[string]any{"name": "csv_column_stats", "description": "Counts CSV rows and computes the sum and mean of one numeric column.",
		"input_schema_json": `{"type":"object","properties":{"file_text":{"type":"string"},"column":{"type":"string"}},"required":["file_text"]}`,
		"source":            source, "tests": tests})
	return string(b)
}

const csvObjective = "Read data/sales.csv and write reports/stats.md with the number of rows and the exact sum and mean of the amount column."

// rs6Model: without the L2 tool the planner cannot compute the statistics and
// returns no plan; the build module drafts the tool (the first draft imports
// os and is repaired); with the tool granted the planner uses it.
func rs6Model(builds *int, mu *sync.Mutex) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "L2 tool"):
			mu.Lock()
			*builds++
			n := *builds
			mu.Unlock()
			if n == 1 {
				return gatewaytest.Reply{Content: csvDraft("import os\n" + csvStatsSource)}
			}
			return gatewaytest.Reply{Content: csvDraft(csvStatsSource)}
		case isSys(req, "planning module"):
			if !strings.Contains(req.Messages[0].Content, "l2.csv_column_stats") {
				return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":null}`}
			}
			return planWith([]string{"reports/stats.md exists"},
				gatewaytest.Step(1, "compute the statistics", "l2.csv_column_stats", map[string]any{"input": `{"column": "amount"}`, "path": "data/sales.csv"}),
				gatewaytest.Step(2, "write the report", "workspace.write", map[string]any{"path": "reports/stats.md"}))
		}
		return goodModel(r, req)
	}
}

// RS-6 / D.1 P4: a missing-tool limitation is resolved end to end through an
// R3 remedy: draft → static check (repair) → sandbox tests and benchmark →
// owner approval bound to the hash → registered → re-test uses it → promoted.
func TestRS6L2ToolRemedyEndToEnd(t *testing.T) {
	var builds int
	var mu sync.Mutex
	h := newHarness(t, harnessOpts{respond: rs6Model(&builds, &mu), sandbox: true})
	ctx := context.Background()
	csv := "item,amount\nalpha,1250000.50\nbeta,980000.25\ngamma,1102345.10\ndelta,750000\n"
	first, _, err := h.eng.Submit(ctx, CreateInput{Objective: csvObjective, Tools: []string{"workspace.write"}, Attachments: map[string]string{"data/sales.csv": csv}})
	if err != nil {
		t.Fatal(err)
	}
	if done := h.wait(first.ID, task.Failed, task.Succeeded); done.State != task.Failed {
		t.Fatalf("without the tool the task should fail: %s", done.State)
	}
	// The tool cannot be granted before it exists and is approved.
	if _, _, err := h.eng.Submit(ctx, CreateInput{Objective: csvObjective, Tools: []string{"workspace.write", "l2.csv_column_stats"}}); err == nil {
		t.Fatal("an unapproved L2 tool was granted")
	}
	cur, _ := task.Get(ctx, h.st.R, first.ID)
	lim, err := h.eng.AddLimitation(ctx, "capability_gap", deref(cur.TaskClass), "no tool computes statistics over a CSV column", "needed for sales reports")
	if err != nil {
		t.Fatal(err)
	}
	h.eng.RunImprovementCycle(ctx)

	tools, _ := h.eng.ListL2(ctx)
	if len(tools) != 1 || tools[0].Status != "awaiting_approval" || builds != 2 {
		t.Fatalf("tools %d builds %d (first draft imported os and had to be repaired)", len(tools), builds)
	}
	tool := tools[0]
	if tool.Report["benchmark"] == nil || tool.Report["static_check"] == nil {
		t.Fatalf("report lacks static check or benchmark: %v", tool.Report)
	}
	if _, ok := policy.Lookup("l2.csv_column_stats"); ok {
		t.Fatal("tool registered before approval")
	}
	if _, err := h.eng.DecideL2(ctx, tool.ID, true, strings.Repeat("0", 64), "owner"); err == nil {
		t.Fatal("approval with the wrong hash accepted")
	}
	if _, err := h.eng.DecideL2(ctx, tool.ID, true, tool.SourceSHA256, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, ok := policy.Lookup("l2.csv_column_stats"); !ok {
		t.Fatal("approved tool not registered")
	}
	defer policy.UnregisterL2Tool("l2.csv_column_stats")

	// The re-test (the failed task again, with the tool granted) uses the tool.
	var retest *task.Task
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		h.eng.RunImprovementCycle(ctx)
		ls, _ := h.eng.ListLimitations(ctx, "")
		for _, l := range ls {
			if l.ID == lim.ID && l.Status == "resolved" {
				goto resolved
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	{
		ls, _ := h.eng.ListLimitations(ctx, "")
		for _, l := range ls {
			rs, _ := h.eng.remediesOf(ctx, l.ID)
			t.Logf("limitation %s %s %s remedies %+v", l.ID, l.Status, l.TaskClass, rs)
		}
		rows, _ := h.st.R.Query("select id, state, coalesce(reason,''), agent_role from tasks order by created_at")
		for rows.Next() {
			var id, st, reason, role string
			rows.Scan(&id, &st, &reason, &role)
			t.Logf("task %s %s %s %s %s", id, role, st, reason, errorDetail(h.events(id)))
		}
		rows.Close()
	}
	t.Fatal("limitation not resolved by the R3 remedy")
resolved:
	rows, _ := h.st.R.Query("select id from tasks where agent_role='retest'")
	for rows.Next() {
		var id string
		rows.Scan(&id)
		retest, _ = task.Get(ctx, h.st.R, id)
	}
	rows.Close()
	if retest == nil || retest.State != task.Succeeded {
		t.Fatal("re-test task missing or not completed")
	}
	evs := h.events(retest.ID)
	used := false
	for _, ev := range evs {
		used = used || (ev.Type == "TOOL_SUCCEEDED" && strings.Contains(string(ev.Data), "l2.csv_column_stats"))
	}
	if !used {
		t.Fatalf("the re-test did not use the tool: %s", eventTypes(evs))
	}
	obs, _ := h.eng.observations(ctx, retest, 3)
	got := ""
	for _, o := range obs {
		got += o.Content
	}
	if !strings.Contains(got, `"rows": 4`) || !strings.Contains(got, `"sum": 4082345.85`) {
		t.Fatalf("tool output: %s", got)
	}
}

// Gates: a draft that fails its own tests, or tries to escape, is refused.
func TestL2Gates(t *testing.T) {
	h := newHarness(t, harnessOpts{sandbox: true})
	ctx := context.Background()
	bad := func(src string) []string {
		var d L2Draft
		json.Unmarshal([]byte(csvDraft(src)), &struct{ *L2Draft }{&d})
		var raw map[string]any
		json.Unmarshal([]byte(csvDraft(src)), &raw)
		d = L2Draft{Name: "csv_column_stats", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`), Source: src}
		for _, tc := range raw["tests"].([]any) {
			m := tc.(map[string]any)
			d.Tests = append(d.Tests, L2Test{Input: json.RawMessage(m["input_json"].(string)), Expected: json.RawMessage(m["expected_json"].(string))})
		}
		_, problems := h.eng.validateL2(ctx, &d)
		return problems
	}
	if p := bad(csvStatsSource); len(p) != 0 {
		t.Fatalf("good tool refused: %v", p)
	}
	for name, src := range map[string]string{
		"wrong result":      strings.Replace(csvStatsSource, "len(rows)", "len(rows) + 1", 1),
		"imports os":        "import os\n" + csvStatsSource,
		"eval":              csvStatsSource + "\nx = eval('1')\n",
		"dunder escape":     csvStatsSource + "\ny = ().__class__\n",
		"open a file":       csvStatsSource + "\ndef g():\n    return open('/etc/passwd').read()\n",
		"no run function":   "def main(input):\n    return 1\n",
		"non-deterministic": strings.Replace(csvStatsSource, `"rows": len(rows)`, `"rows": len(rows) + __import__("random").random()`, 1),
	} {
		if p := bad(src); len(p) == 0 {
			t.Fatalf("%s: accepted", name)
		}
	}
}
