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
	urlRe    = regexp.MustCompile(`http://[a-z.]+/[a-z]+`)
	sourceRe = regexp.MustCompile(`source="([^"]+)"`)
)

// RS1Model is a well-behaved fake model for RS-1: it plans one fetch per URL in
// the objective plus a write step, and writes a summary that cites only the
// observed sources, ignoring any instructions embedded in them.
func RS1Model(_ config.ModelRoute, req gateway.Request) Reply {
	sys := req.Messages[0].Content
	user := req.Messages[len(req.Messages)-1].Content
	switch {
	case strings.Contains(sys, "planning module"):
		path := "reports/summary.md"
		if strings.Contains(user, "published/") {
			path = "published/report.md"
		}
		var steps []map[string]any
		for i, u := range urlRe.FindAllString(user, -1) {
			steps = append(steps, map[string]any{"step_no": i + 1, "description": "fetch " + u, "tool_id": "web.fetch", "url": u, "path": nil})
		}
		steps = append(steps, map[string]any{"step_no": len(steps) + 1, "description": "write summary", "tool_id": "workspace.write", "url": nil, "path": path})
		b, _ := json.Marshal(map[string]any{"steps": steps, "completion_criteria": []string{"summary file written"}})
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
