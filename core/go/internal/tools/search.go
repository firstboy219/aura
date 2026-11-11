package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrSearch = errors.New("search_error")

// SearchResult is one web search hit. All fields are untrusted.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Searcher is a web.search provider (ADR-0021).
type Searcher interface {
	Name() string
	Search(ctx context.Context, query string, n int) ([]SearchResult, error)
}

// userAgent identifies AURA to search APIs (Wikimedia requires a descriptive one).
const userAgent = "AURA/0.2 (autonomous agent runtime; owner-operated)"

// apiClient fetches JSON from one fixed provider host through the SSRF guard.
type apiClient struct {
	guard   *Guard
	host    string
	timeout time.Duration
}

func (c *apiClient) getJSON(ctx context.Context, rawURL string, headers map[string]string, v any) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() != c.host {
		return fmt.Errorf("%w: provider URL must be https://%s", ErrSearch, c.host)
	}
	client := &http.Client{Timeout: c.timeout, Transport: &http.Transport{Proxy: nil, DialContext: c.guard.DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSearch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: %s returned HTTP %d", ErrSearch, c.host, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(v)
}

var reTags = regexp.MustCompile(`<[^>]*>`)

func cleanSnippet(s string) string {
	return strings.TrimSpace(html.UnescapeString(reTags.ReplaceAllString(s, "")))
}

// Wikipedia searches one language edition through the official API (no key).
type Wikipedia struct {
	api  apiClient
	lang string
}

func NewWikipedia(g *Guard, lang string) *Wikipedia {
	if lang == "" {
		lang = "en"
	}
	return &Wikipedia{api: apiClient{guard: g, host: lang + ".wikipedia.org", timeout: 15 * time.Second}, lang: lang}
}

func (w *Wikipedia) Name() string { return "wikipedia" }

func (w *Wikipedia) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	q := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {query}, "format": {"json"}, "utf8": {"1"},
		"srlimit": {fmt.Sprint(n)}}
	var out struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := w.api.getJSON(ctx, "https://"+w.api.host+"/w/api.php?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	var res []SearchResult
	for _, s := range out.Query.Search {
		page := strings.ReplaceAll(s.Title, " ", "_")
		res = append(res, SearchResult{Title: s.Title, URL: "https://" + w.api.host + "/wiki/" + url.PathEscape(page),
			Snippet: cleanSnippet(s.Snippet)})
	}
	return res, nil
}

// Multi asks several providers and merges their results, one from each in turn
// (ADR-0062: the fallback for the Chromium searcher covers both Indonesian and
// English Wikipedia). A provider that fails is skipped while another answers.
type Multi []Searcher

func (m Multi) Name() string { return "wikipedia" }

func (m Multi) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	var lists [][]SearchResult
	var errs []error
	for _, s := range m {
		r, err := s.Search(ctx, query, n)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		lists = append(lists, r)
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrSearch, errors.Join(errs...))
	}
	var out []SearchResult
	for i := 0; len(out) < n; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) && len(out) < n {
				out = append(out, l[i])
				added = true
			}
		}
		if !added {
			break
		}
	}
	return out, nil
}

// Brave uses the Brave Search API (key from a secret file).
type Brave struct {
	api apiClient
	key string
}

func NewBrave(g *Guard, key string) *Brave {
	return &Brave{api: apiClient{guard: g, host: "api.search.brave.com", timeout: 15 * time.Second}, key: key}
}

func (b *Brave) Name() string { return "brave" }

func (b *Brave) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	q := url.Values{"q": {query}, "count": {fmt.Sprint(n)}}
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := b.api.getJSON(ctx, "https://api.search.brave.com/res/v1/web/search?"+q.Encode(),
		map[string]string{"X-Subscription-Token": b.key}, &out); err != nil {
		return nil, err
	}
	var res []SearchResult
	for _, r := range out.Web.Results {
		res = append(res, SearchResult{Title: cleanSnippet(r.Title), URL: r.URL, Snippet: cleanSnippet(r.Description)})
	}
	return res, nil
}
