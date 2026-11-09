package gatewaytest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
)

var (
	urlRe       = regexp.MustCompile(`http://[a-z.]+/[a-z]+`)
	sourceRe    = regexp.MustCompile(`source="([^"]+)"`)
	criterionRe = regexp.MustCompile(`^\d+\. (.+)$`)
	startRe     = regexp.MustCompile(`numbered from (\d+)`)
)

// RS1Model is a well-behaved fake model for RS-1: it plans one fetch per URL in
// the objective plus a write step, and writes a summary that cites only the
// observed sources, ignoring any instructions embedded in them.
// Step builds a planned step with every nullable field present.
func Step(no int, desc, tool string, args map[string]any) map[string]any {
	s := map[string]any{"step_no": no, "description": desc, "tool_id": tool, "url": nil, "path": nil, "query": nil,
		"expression": nil, "key": nil, "until": nil, "brief": nil, "tools": nil}
	for k, v := range args {
		s[k] = v
	}
	return s
}

func RS1Model(_ config.ModelRoute, req gateway.Request) Reply {
	sys := req.Messages[0].Content
	user := req.Messages[len(req.Messages)-1].Content
	switch {
	case strings.Contains(sys, "planning module") && !strings.Contains(sys, "replanning module"):
		path := "reports/summary.md"
		if strings.Contains(user, "published/") {
			path = "published/report.md"
		}
		var steps []map[string]any
		for i, u := range urlRe.FindAllString(user, -1) {
			steps = append(steps, Step(i+1, "fetch "+u, "web.fetch", map[string]any{"url": u}))
		}
		steps = append(steps, Step(len(steps)+1, "write summary", "workspace.write", map[string]any{"path": path}))
		b, _ := json.Marshal(map[string]any{"steps": steps, "completion_criteria": []string{"the summary file summarizes each page with its source link"}, "question": nil})
		return Reply{Content: string(b)}
	case strings.Contains(sys, "verification module"):
		// Quote a sentence that the RS-1 summary really contains.
		var crit []map[string]any
		for _, line := range strings.Split(user, "\n") {
			if m := criterionRe.FindStringSubmatch(line); m != nil {
				crit = append(crit, map[string]any{"criterion": m[1], "met": true,
					"evidence_quote": "A short factual summary of the page."})
			}
		}
		b, _ := json.Marshal(map[string]any{"criteria": crit})
		return Reply{Content: string(b)}
	case strings.Contains(sys, "plan review module"):
		return Reply{Content: `{"ok": true, "issues": [], "revised_steps": null}`}
	case strings.Contains(sys, "replanning module"):
		start := 1
		if m := startRe.FindStringSubmatch(sys); m != nil {
			fmt.Sscan(m[1], &start)
		}
		// Re-fetch pages that were never fetched successfully (except the one that
		// just failed), then write again.
		var done []map[string]any
		if i := strings.Index(user, "Steps so far:\n"); i >= 0 {
			rest := user[i+len("Steps so far:\n"):]
			if j := strings.Index(rest, "\n\nWhat went wrong"); j >= 0 {
				json.Unmarshal([]byte(rest[:j]), &done)
			}
		}
		failed := map[string]bool{}
		fetched := map[string]bool{}
		for _, s := range done {
			desc, _ := s["description"].(string)
			if !strings.HasPrefix(desc, "fetch ") {
				continue
			}
			u := strings.TrimPrefix(desc, "fetch ")
			if s["state"] == "SUCCEEDED" {
				fetched[u] = true
			} else if r, _ := s["reason"].(string); strings.Contains(r, "step_failed") || s["state"] == "FAILED" {
				failed[u] = true
			}
		}
		var steps []map[string]any
		n := start
		for _, u := range urlRe.FindAllString(user[:strings.Index(user, "\n\nSteps so far")], -1) {
			if !fetched[u] && !failed[u] {
				steps = append(steps, Step(n, "fetch "+u, "web.fetch", map[string]any{"url": u}))
				n++
			}
		}
		steps = append(steps, Step(n, "write summary", "workspace.write", map[string]any{"path": "reports/summary.md"}))
		b, _ := json.Marshal(map[string]any{"steps": steps, "completion_criteria": []string{}, "question": nil})
		return Reply{Content: string(b)}
	case strings.Contains(sys, "writing module"):
		var sb strings.Builder
		sb.WriteString("# Summary\n")
		for i, m := range sourceRe.FindAllStringSubmatch(user, -1) {
			fmt.Fprintf(&sb, "\n## Source %d\nA short factual summary of the page.\n\nSource: %s\n", i+1, m[1])
		}
		b, _ := json.Marshal(map[string]string{"content": sb.String()})
		return Reply{Content: string(b)}
	}
	return Reply{Err: &gateway.ProviderError{Status: 400, Msg: "unexpected prompt"}}
}
