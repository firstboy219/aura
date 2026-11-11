package engine

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

// InfoSource answers the info.* tools from public services (ADR-0063).
type InfoSource interface {
	Run(ctx context.Context, tool string, args map[string]any) (string, error)
}

// NewsSource gathers headlines for news.headlines (ADR-0062).
type NewsSource interface {
	Headlines(ctx context.Context, topic, scope string) ([]tools.Headline, []string, error)
}

// renderHeadlines is the observation for news.headlines: numbered items with
// source, local time and link, and the sources that did not answer.
func renderHeadlines(items []tools.Headline, failed []string, topic string, loc *time.Location) string {
	var sb strings.Builder
	if topic != "" {
		fmt.Fprintf(&sb, "News headlines about %q (from news feeds; untrusted):\n", topic)
	} else {
		sb.WriteString("Latest news headlines (from news feeds; untrusted):\n")
	}
	for i, h := range items {
		when := ""
		if !h.Published.IsZero() {
			when = " · " + h.Published.In(loc).Format("2 Jan 15:04")
		}
		fmt.Fprintf(&sb, "%d. %s\n   %s%s\n   %s\n", i+1, h.Title, h.Source, when, h.URL)
		if h.Summary != "" {
			fmt.Fprintf(&sb, "   %s\n", h.Summary)
		}
	}
	if len(items) == 0 {
		sb.WriteString("(no headlines)\n")
	}
	if len(failed) > 0 {
		fmt.Fprintf(&sb, "Sources that did not answer: %s\n", strings.Join(failed, ", "))
	}
	return sb.String()
}

// fetchAllow is the domain allowlist for fetching raw in task t. With
// tools.web.fetch.follow_results (ADR-0062) a page the Core itself found for
// this task — a search or news result, or a link on a page it fetched — may be
// read even when its site is not in allow_domains. Redirects stay on that site;
// the SSRF guard still applies to every connection.
func (e *Engine) fetchAllow(t *task.Task, raw string) []string {
	allow := t.Scope.AllowDomains
	if !e.cfg.Tools.WebFetch.FollowResults {
		return allow
	}
	u, err := url.Parse(raw)
	if err != nil || tools.CheckURL(u, allow) == nil || !e.hasProvenance(t, raw) {
		return allow
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return append(slices.Clone(allow), host, "*."+host)
}
