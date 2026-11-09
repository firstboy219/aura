package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

type fakeSearcher struct{ results []tools.SearchResult }

func (f fakeSearcher) Name() string { return "fake" }
func (f fakeSearcher) Search(context.Context, string, int) ([]tools.SearchResult, error) {
	return f.results, nil
}

func planReply(steps ...map[string]any) gatewaytest.Reply {
	return planWith([]string{}, steps...)
}

func planWith(criteria []string, steps ...map[string]any) gatewaytest.Reply {
	b, _ := json.Marshal(map[string]any{"steps": steps, "completion_criteria": criteria, "question": nil})
	return gatewaytest.Reply{Content: string(b)}
}

func researchModel(pick string) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "planning module"):
			return planReply(
				gatewaytest.Step(1, "search", "web.search", map[string]any{"query": "solar power"}),
				gatewaytest.Step(2, "fetch the best result", "web.fetch", nil),
				gatewaytest.Step(3, "write", "workspace.write", map[string]any{"path": "reports/summary.md"}))
		case isSys(req, "You choose which URL"):
			return gatewaytest.Reply{Content: `{"url":"` + pick + `"}`}
		}
		return goodModel(r, req)
	}
}

// Research: search → fetch a result chosen at step time → write. The chosen URL
// is accepted because the Core recorded it as a search result (ADR-0017).
func TestDynamicFetchWithProvenance(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: researchModel("http://fixture.test/one"),
		searcher: fakeSearcher{[]tools.SearchResult{{Title: "Solar", URL: "http://fixture.test/one", Snippet: "solar"}}}})
	tk := h.submit("Search the web about solar power and write reports/summary.md", nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s %s", done.State, deref(done.Reason), errorDetail(h.events(tk.ID)))
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.Workspace.Root, tk.ID, "reports", "summary.md"))
	if !strings.Contains(string(b), "http://fixture.test/one") {
		t.Fatalf("summary lacks the fetched source:\n%s", b)
	}
}

// A URL the Core never recorded (not a search result, not a page link) is
// denied even though its domain is allowlisted.
func TestDynamicFetchWithoutProvenanceDenied(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: researchModel("http://fixture.test/three?leak=secret"),
		searcher: fakeSearcher{[]tools.SearchResult{{Title: "Solar", URL: "http://fixture.test/one"}}}})
	tk := h.submit("Search the web about solar power and write reports/summary.md", nil)
	done := h.wait(tk.ID, task.Failed, task.Succeeded)
	if done.State != task.Failed || !strings.Contains(strings.Join(deniedReasons(t, h, tk.ID), ","), "argument_without_provenance") {
		t.Fatalf("%s %s denials=%v", done.State, deref(done.Reason), deniedReasons(t, h, tk.ID))
	}
}

var calcObs = regexp.MustCompile(`= ([0-9.]+) \(exact\)`)

// data.read on an owner attachment + calc with a step-time expression: the
// reported number is computed exactly by the Core.
func TestDataReadAndCalcOnAttachment(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "planning module"):
			return planWith([]string{"reports/total.md states the total amount"},
				gatewaytest.Step(1, "read sales", "data.read", map[string]any{"path": "data/sales.csv"}),
				gatewaytest.Step(2, "total amount", "calc", nil),
				gatewaytest.Step(3, "write total", "workspace.write", map[string]any{"path": "reports/total.md"}))
		case isSys(req, "verification module"):
			return gatewaytest.Reply{Content: `{"criteria":[{"criterion":"reports/total.md states the total amount","met":true,"evidence_quote":"Total sales: 2230000.75"}]}`}
		case isSys(req, "arithmetic expression"):
			return gatewaytest.Reply{Content: `{"expression":"1250000.50 + 980000.25"}`}
		case isSys(req, "writing module"):
			m := calcObs.FindStringSubmatch(req.Messages[len(req.Messages)-1].Content)
			total := "unknown"
			if m != nil {
				total = m[1]
			}
			b, _ := json.Marshal(map[string]string{"content": "Total sales: " + total + "\n"})
			return gatewaytest.Reply{Content: string(b)}
		}
		return goodModel(r, req)
	}})
	in := CreateInput{Objective: "Read data/sales.csv, compute the total amount and write reports/total.md",
		AllowDomains: []string{h.host}, Attachments: map[string]string{"data/sales.csv": "region,amount\nJakarta,1250000.50\nBandung,980000.25\n"}}
	tk, _, err := h.eng.Submit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s %s", done.State, deref(done.Reason), errorDetail(h.events(tk.ID)))
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.Workspace.Root, tk.ID, "reports", "total.md"))
	if string(b) != "Total sales: 2230000.75\n" {
		t.Fatalf("%q", b)
	}
	bad := []CreateInput{
		{Objective: "x", Attachments: map[string]string{"../x": "a"}},
		{Objective: "x", Attachments: map[string]string{".aura/x": "a"}},
		{Objective: "x", Attachments: map[string]string{"big.txt": strings.Repeat("a", 300<<10)}},
	}
	for _, b := range bad {
		if _, _, err := h.eng.Submit(context.Background(), b); err == nil {
			t.Errorf("accepted %v", b.Attachments)
		}
	}
}
