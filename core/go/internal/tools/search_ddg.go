package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DuckDuckGoHTML searches through DuckDuckGo's plain HTML endpoint with an ordinary HTTP request (no key, no browser). Added 2026-11-14 (ADR-0068): the
// isolated Chromium is served unrelated results by Bing and a 403 by DuckDuckGo, so web.search found nothing and every task that needed the web
// (hotel prices, job lists, news) failed verification. This plain request is answered normally. Results are untrusted data like any other.
type DuckDuckGoHTML struct {
	guard   *Guard
	timeout time.Duration
}

func NewDuckDuckGoHTML(g *Guard) *DuckDuckGoHTML {
	return &DuckDuckGoHTML{guard: g, timeout: 15 * time.Second}
}

func (d *DuckDuckGoHTML) Name() string { return "duckduckgo-html" }

var (
	reDDGResult = regexp.MustCompile(`(?s)class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>.*?class="result__snippet"[^>]*>(.*?)</a>`)
)

// ParseDuckDuckGoHTML reads the results of the HTML endpoint's page: advertisements skipped, redirect links unwrapped, https only (pure; unit-tested).
func ParseDuckDuckGoHTML(page string, n int) []SearchResult {
	var out []SearchResult
	seen := map[string]bool{}
	for _, m := range reDDGResult.FindAllStringSubmatch(page, -1) {
		href := html.UnescapeString(m[1])
		if strings.Contains(href, "duckduckgo.com/y.js") {
			continue
		}
		if strings.HasPrefix(href, "//") {
			href = "https:" + href
		}
		if strings.Contains(href, "uddg=") {
			u, err := url.Parse(href)
			if err != nil {
				continue
			}
			href = u.Query().Get("uddg")
		}
		pu, err := url.Parse(href)
		if err != nil || pu.Scheme != "https" || pu.Host == "" || pu.User != nil || seen[href] {
			continue
		}
		seen[href] = true
		out = append(out, SearchResult{Title: oneLineText(cleanSnippet(m[2]), 300), URL: href, Snippet: oneLineText(cleanSnippet(m[3]), 600)})
		if len(out) >= n {
			break
		}
	}
	return out
}

func oneLineText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

func (d *DuckDuckGoHTML) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	client := pageClient(d.guard, d.timeout)
	form := url.Values{"q": {query}, "kl": {"id-id"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	pageHeaders(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSearch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: html.duckduckgo.com returned HTTP %d", ErrSearch, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSearch, err)
	}
	return ParseDuckDuckGoHTML(string(body), n), nil
}

// FirstWithResults tries its searchers in order and returns the first answer that has results; when none has, the last empty answer or the errors.
type FirstWithResults []Searcher

func (f FirstWithResults) Name() string { return "web" }

func (f FirstWithResults) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	var errs []string
	var empty bool
	for _, s := range f {
		r, err := s.Search(ctx, query, n)
		if err != nil {
			errs = append(errs, s.Name()+": "+err.Error())
			continue
		}
		if len(r) > 0 {
			return r, nil
		}
		empty = true
	}
	if empty {
		return nil, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrSearch, strings.Join(errs, "; "))
}
