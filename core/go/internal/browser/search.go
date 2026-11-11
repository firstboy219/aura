package browser

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"aura.local/core/internal/egress"
	"aura.local/core/internal/tools"
)

// Searcher is web.search through the isolated Chromium (ADR-0062): it opens
// a search engine's result page in a fresh browser session, reads the results
// with the host's fixed selectors and closes the session. The session may only
// reach the engine's own domains through the Core's egress proxy, so a result
// page cannot pull the browser anywhere else. Results are untrusted data.
type Searcher struct {
	c        *Client
	proxy    *egress.Proxy
	engines  []string
	fallback tools.Searcher // used when every engine fails or finds nothing; may be nil
}

type engineSpec struct {
	url     func(q string) string
	domains []string
	own     func(host string) bool // links back into the engine itself are not results
}

var engines = map[string]engineSpec{
	"bing": {
		// No market or country parameter: on 2026-11-11 any of cc/mkt/setlang made Bing serve an empty result page
		// to the headless browser.
		url:     func(q string) string { return "https://www.bing.com/search?" + url.Values{"q": {q}}.Encode() },
		domains: []string{"www.bing.com", "bing.com", "*.bing.com"},
		own: func(h string) bool {
			return h == "bing.com" || strings.HasSuffix(h, ".bing.com") || h == "go.microsoft.com"
		},
	},
	"duckduckgo": {
		url:     func(q string) string { return "https://html.duckduckgo.com/html/?" + url.Values{"q": {q}}.Encode() },
		domains: []string{"html.duckduckgo.com"},
		own:     func(h string) bool { return h == "duckduckgo.com" || strings.HasSuffix(h, ".duckduckgo.com") },
	},
}

// Engines lists the search engines the Chromium searcher can read.
func Engines() []string { return []string{"bing", "duckduckgo"} }

func NewSearcher(c *Client, p *egress.Proxy, names []string, fallback tools.Searcher) *Searcher {
	return &Searcher{c: c, proxy: p, engines: names, fallback: fallback}
}

func (s *Searcher) Name() string { return "chromium" }

func (s *Searcher) Search(ctx context.Context, query string, n int) ([]tools.SearchResult, error) {
	var errs []error
	for _, name := range s.engines {
		res, err := s.one(ctx, name, query, n)
		if err == nil && len(res) > 0 {
			return res, nil
		}
		if err == nil {
			err = fmt.Errorf("%s: no results", name)
		}
		errs = append(errs, err)
	}
	if s.fallback != nil {
		if res, err := s.fallback.Search(ctx, query, n); err == nil {
			return res, nil
		} else {
			errs = append(errs, err)
		}
	}
	return nil, fmt.Errorf("%w: %v", tools.ErrSearch, errors.Join(errs...))
}

type serpResult struct {
	PageState
	Results []struct {
		Title   string `json:"title"`
		Href    string `json:"href"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

func (s *Searcher) one(ctx context.Context, name, query string, n int) ([]tools.SearchResult, error) {
	spec, ok := engines[name]
	if !ok {
		return nil, fmt.Errorf("unknown engine %q", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	b := make([]byte, 12)
	rand.Read(b)
	id := "q-" + hex.EncodeToString(b)
	tok := make([]byte, 24)
	rand.Read(tok)
	s.proxy.Add(&egress.Session{ID: id, Token: hex.EncodeToString(tok), AllowDomain: spec.domains})
	defer s.proxy.Remove(id)
	if err := s.c.Call(ctx, "session.open", map[string]any{"session_id": id, "proxy_token": hex.EncodeToString(tok)}, nil); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	defer s.c.Call(context.WithoutCancel(ctx), "session.close", map[string]any{"session_id": id}, nil)
	var st PageState
	if err := s.c.Call(ctx, "page.goto", map[string]any{"session_id": id, "url": spec.url(query)}, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var out serpResult
	if err := s.c.Call(ctx, "page.serp", map[string]any{"session_id": id, "engine": name}, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if out.SiteBlocked || st.SiteBlocked {
		return nil, fmt.Errorf("%s: the engine refused the request (bot check)", name)
	}
	if !relevantResults(query, out.Results) {
		// Engines answer some automated browsers with results that have nothing to do with the query (measured
		// 2026-11-11: Chinese forum pages for an Indonesian query). Such results are never handed to the model.
		return nil, fmt.Errorf("%s: the results do not match the query", name)
	}
	var res []tools.SearchResult
	seen := map[string]bool{}
	for _, r := range out.Results {
		u := ResultURL(r.Href)
		if u == "" || seen[u] {
			continue
		}
		pu, _ := url.Parse(u)
		if spec.own(strings.ToLower(pu.Hostname())) {
			continue
		}
		seen[u] = true
		res = append(res, tools.SearchResult{Title: oneLine(r.Title, 300), URL: u, Snippet: oneLine(r.Snippet, 600)})
		if len(res) >= n {
			break
		}
	}
	return res, nil
}

var stopWords = map[string]bool{"yang": true, "dengan": true, "untuk": true, "dalam": true, "dari": true, "pada": true, "adalah": true,
	"hari": true, "ini": true, "itu": true, "apa": true, "bagaimana": true, "berapa": true, "sebutkan": true, "beserta": true,
	"menurut": true, "tentang": true, "saya": true, "tolong": true, "carikan": true, "cari": true, "dan": true, "atau": true,
	"sumbernya": true, "the": true, "and": true, "for": true, "with": true, "what": true, "how": true, "about": true, "from": true}

func queryTokens(q string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) }) {
		if len([]rune(f)) >= 4 && !stopWords[f] {
			out = append(out, f)
		}
	}
	return out
}

// relevantResults reports whether the visible results look like answers to the
// query: some result mentions at least two of the query's significant words
// (one when the query has only one or two) in its title, snippet or address.
func relevantResults(query string, results []struct {
	Title   string `json:"title"`
	Href    string `json:"href"`
	Snippet string `json:"snippet"`
}) bool {
	tokens := queryTokens(query)
	if len(tokens) == 0 {
		return true
	}
	need := 2
	if len(tokens) < 3 {
		need = 1
	}
	for _, r := range results {
		text := strings.ToLower(r.Title + " " + r.Snippet + " " + r.Href)
		hits := 0
		for _, t := range tokens {
			if strings.Contains(text, t) {
				hits++
			}
		}
		if hits >= need {
			return true
		}
	}
	return false
}

// ResultURL turns a result link as it appears on an engine's page into the
// address it points to: engine redirect links are unwrapped (Bing's ck/a with
// a base64 "u", DuckDuckGo's l/?uddg=). Anything that is not a plain absolute
// http(s) URL without credentials is dropped.
func ResultURL(href string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case (host == "www.bing.com" || host == "bing.com") && strings.HasPrefix(u.Path, "/ck/a"):
		enc := u.Query().Get("u")
		if !strings.HasPrefix(enc, "a1") {
			return ""
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(enc[2:], "="))
		if err != nil {
			return ""
		}
		return ResultURL(string(raw))
	case strings.HasSuffix(host, "duckduckgo.com") && u.Path == "/l/":
		return ResultURL(u.Query().Get("uddg"))
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func oneLine(s string, max int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > max {
		r = r[:max]
	}
	return string(r)
}
