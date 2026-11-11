package engine

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

type fakeNews struct {
	items []tools.Headline
	topic string
}

func (f *fakeNews) Headlines(_ context.Context, topic, _ string) ([]tools.Headline, []string, error) {
	f.topic = topic
	return f.items, []string{"Tempo"}, nil
}

func newsModel(pick string) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "planning module"):
			return planReply(
				gatewaytest.Step(1, "headlines", "news.headlines", map[string]any{"query": "harga emas"}),
				gatewaytest.Step(2, "read the main article", "web.fetch", nil),
				gatewaytest.Step(3, "write", "workspace.write", map[string]any{"path": "reports/news.md"}))
		case isSys(req, "You choose which URL"):
			return gatewaytest.Reply{Content: `{"url":"` + pick + `"}`}
		case isSys(req, "replanning module"):
			start := 1
			if m := regexp.MustCompile(`numbered from (\d+)`).FindStringSubmatch(req.Messages[0].Content); m != nil {
				start, _ = strconv.Atoi(m[1])
			}
			return planReply(gatewaytest.Step(start, "write", "workspace.write", map[string]any{"path": "reports/news.md"}))
		}
		return goodModel(r, req)
	}
}

func newsItems() []tools.Headline {
	return []tools.Headline{{Title: "Harga emas naik", URL: "http://news.other.test/one", Source: "ANTARA", Published: time.Now()}}
}

// News headlines become an observation and their links provenance; with
// follow_results the article on a site outside allow_domains can be read.
func TestNewsHeadlinesAndFollowingTheirLinks(t *testing.T) {
	n := &fakeNews{items: newsItems()}
	h := newHarness(t, harnessOpts{respond: newsModel("http://news.other.test/one"), news: n, follow: true})
	tk := h.submitTools("Cek berita harga emas dan tulis reports/news.md", "news.headlines", "web.fetch", "workspace.write")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s %s", done.State, deref(done.Reason), errorDetail(h.events(tk.ID)))
	}
	if n.topic != "harga emas" || !fetched(h, tk.ID, "news.other.test/one") {
		t.Fatalf("topic %q, fetched=%v", n.topic, fetched(h, tk.ID, "news.other.test/one"))
	}
}

// Without follow_results the same article stays out of reach: its site is not allowlisted.
func TestNewsLinkOutsideAllowlistNeedsFollowResults(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: newsModel("http://news.other.test/one"), news: &fakeNews{items: newsItems()}})
	tk := h.submitTools("Cek berita harga emas dan tulis reports/news.md", "news.headlines", "web.fetch", "workspace.write")
	h.wait(tk.ID, task.Succeeded, task.Failed)
	if fetched(h, tk.ID, "news.other.test/one") || !stepFailed(h, tk.ID) {
		t.Fatalf("article fetched without follow_results; denials=%v", deniedReasons(t, h, tk.ID))
	}
}

// follow_results never opens a URL the Core did not record for the task.
func TestFollowResultsStillNeedsProvenance(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: newsModel("http://evil.other.test/one?leak=1"), news: &fakeNews{items: newsItems()}, follow: true})
	tk := h.submitTools("Cek berita harga emas dan tulis reports/news.md", "news.headlines", "web.fetch", "workspace.write")
	h.wait(tk.ID, task.Succeeded, task.Failed)
	if fetched(h, tk.ID, "leak=1") {
		t.Fatal("a URL without provenance was fetched")
	}
}

func TestRenderHeadlinesNamesSilentSources(t *testing.T) {
	out := renderHeadlines(newsItems(), []string{"Tempo"}, "", time.UTC)
	if !strings.Contains(out, "1. Harga emas naik") || !strings.Contains(out, "ANTARA") || !strings.Contains(out, "did not answer: Tempo") {
		t.Fatal(out)
	}
}
