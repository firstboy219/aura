package tools

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// News gathers headlines from a fixed list of news feeds (ADR-0062). It reads
// RSS only — no model call, no page JavaScript — from the feeds named here,
// through the SSRF guard, and never follows a redirect to another host. All
// item fields are untrusted data.
type News struct {
	guard     *Guard
	timeout   time.Duration
	feeds     []Feed
	google    string // Google News edition parameters, e.g. "hl=id&gl=ID&ceid=ID:id"
	now       func() time.Time
	tlsConfig *tls.Config // tests only
}

// Feed is one news source.
type Feed struct {
	Name  string
	URL   string
	World bool // international rather than Indonesian news
}

// DefaultFeeds are the publishers read for general headlines.
var DefaultFeeds = []Feed{
	{Name: "ANTARA", URL: "https://www.antaranews.com/rss/terkini.xml"},
	{Name: "CNBC Indonesia", URL: "https://www.cnbcindonesia.com/news/rss"},
	{Name: "CNN Indonesia", URL: "https://www.cnnindonesia.com/nasional/rss"},
	{Name: "Tempo", URL: "https://rss.tempo.co/nasional"},
	{Name: "Republika", URL: "https://www.republika.co.id/rss"},
	{Name: "BBC Indonesia", URL: "https://feeds.bbci.co.uk/indonesia/rss.xml"},
	{Name: "BBC World", URL: "https://feeds.bbci.co.uk/news/world/rss.xml", World: true},
	{Name: "Al Jazeera", URL: "https://www.aljazeera.com/xml/rss/all.xml", World: true},
	{Name: "CNBC", URL: "https://www.cnbc.com/id/100003114/device/rss/rss.html", World: true},
}

// Headline is one news item.
type Headline struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Source    string    `json:"source"`
	Summary   string    `json:"summary,omitempty"`
	Published time.Time `json:"published,omitzero"`
}

const (
	maxFeedBytes  = 3 << 20
	maxHeadlines  = 30
	freshHeadline = 36 * time.Hour
)

func NewNews(g *Guard, feeds []Feed) *News {
	if feeds == nil {
		feeds = DefaultFeeds
	}
	return &News{guard: g, timeout: 12 * time.Second, feeds: feeds, google: "hl=id&gl=ID&ceid=ID:id", now: time.Now}
}

// Headlines returns recent headlines. With a topic, Google News is searched and
// publisher items are kept only when they mention the topic; scope is "id",
// "world" or "" (both).
func (n *News) Headlines(ctx context.Context, topic, scope string) ([]Headline, []string, error) {
	topic = strings.TrimSpace(topic)
	type job struct {
		name, url string
		google    bool
	}
	var jobs []job
	for _, f := range n.feeds {
		if (scope == "id" && f.World) || (scope == "world" && !f.World) {
			continue
		}
		jobs = append(jobs, job{f.Name, f.URL, false})
	}
	if topic != "" {
		jobs = append(jobs, job{"Google News", "https://news.google.com/rss/search?q=" + url.QueryEscape(topic) + "&" + n.google, true})
	} else if scope != "world" {
		jobs = append(jobs, job{"Google News", "https://news.google.com/rss?" + n.google, true})
	}
	var mu sync.Mutex
	var all []Headline
	var failed []string
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := n.feed(ctx, j.url, j.name, j.google)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, j.name)
				return
			}
			all = append(all, items...)
		}()
	}
	wg.Wait()
	if len(all) == 0 && len(failed) > 0 {
		return nil, failed, fmt.Errorf("%w: no news source answered (%s)", ErrSearch, strings.Join(failed, ", "))
	}
	words := topicWords(topic)
	now := n.now()
	seen := map[string]bool{}
	var out []Headline
	for _, h := range all {
		key := strings.ToLower(h.Title)
		if seen[key] {
			continue
		}
		if len(words) > 0 && h.Source != "Google News" && !strings.HasPrefix(h.Source, "Google News:") && !mentions(h, words) {
			continue
		}
		if topic == "" && !h.Published.IsZero() && now.Sub(h.Published) > freshHeadline {
			continue
		}
		seen[key] = true
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	if len(out) > maxHeadlines {
		out = out[:maxHeadlines]
	}
	sort.Strings(failed)
	return out, failed, nil
}

func topicWords(s string) []string {
	var w []string
	for _, x := range strings.Fields(strings.ToLower(s)) {
		if len([]rune(x)) > 2 {
			w = append(w, x)
		}
	}
	return w
}

func mentions(h Headline, words []string) bool {
	text := strings.ToLower(h.Title + " " + h.Summary)
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

type rss struct {
	Channel struct {
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			Source      struct {
				Name string `xml:",chardata"`
			} `xml:"source"`
		} `xml:"item"`
	} `xml:"channel"`
}

func (n *News) feed(ctx context.Context, raw, name string, google bool) ([]Headline, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("%w: bad feed URL", ErrSearch)
	}
	host := u.Hostname()
	client := &http.Client{Timeout: n.timeout, Transport: &http.Transport{Proxy: nil, DialContext: n.guard.DialContext, TLSClientConfig: n.tlsConfig},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 3 || r.URL.Scheme != "https" || r.URL.Hostname() != host {
				return http.ErrUseLastResponse
			}
			return nil
		}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrSearch, name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: %s returned HTTP %d", ErrSearch, name, resp.StatusCode)
	}
	return ParseFeed(io.LimitReader(resp.Body, maxFeedBytes), name, google)
}

// ParseFeed reads RSS items. Google News items name their publisher in
// <source>; their link is Google's own article link.
func ParseFeed(r io.Reader, name string, google bool) ([]Headline, error) {
	var doc rss
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %s: not a readable feed", ErrSearch, name)
	}
	var out []Headline
	for _, it := range doc.Channel.Items {
		title := oneLine(cleanSnippet(it.Title), 300)
		link := strings.TrimSpace(it.Link)
		lu, err := url.Parse(link)
		if title == "" || err != nil || lu.Scheme != "https" && lu.Scheme != "http" || lu.Host == "" || lu.User != nil {
			continue
		}
		src := name
		summary := oneLine(cleanSnippet(it.Description), 300)
		if google {
			if s := oneLine(it.Source.Name, 80); s != "" {
				src = "Google News: " + s
				title = strings.TrimSuffix(title, " - "+s)
			}
			summary = "" // Google's description repeats the title with markup
		}
		h := Headline{Title: title, URL: link, Source: src, Summary: summary}
		for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", time.RFC3339} {
			if t, err := time.Parse(layout, strings.TrimSpace(it.PubDate)); err == nil {
				h.Published = t
				break
			}
		}
		out = append(out, h)
	}
	return out, nil
}

func oneLine(s string, max int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}
