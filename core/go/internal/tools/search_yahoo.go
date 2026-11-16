package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// YahooHTML searches through Yahoo's result page with an ordinary HTTP request (no key, no browser). Yahoo's results come from Bing's index, which is what the owner asked for
// ("cari dari Google, Bing, dll", 2026-11-16): the isolated Chromium is served unrelated results by Bing and a captcha by Google from this server's address, and a plain request
// to Bing returns a script page, but Yahoo answers a plain request with Bing-quality results. Untrusted data like any search result (ADR-0068).
type YahooHTML struct {
	guard   *Guard
	timeout time.Duration
}

func NewYahooHTML(g *Guard) *YahooHTML { return &YahooHTML{guard: g, timeout: 15 * time.Second} }

func (y *YahooHTML) Name() string { return "yahoo-html" }

var (
	reYahooTitle   = regexp.MustCompile(`(?s)<h3[^>]*class="title"[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*?(?:aria-label="([^"]*)")?[^>]*>(.*?)</a>`)
	reYahooSnippet = regexp.MustCompile(`(?s)<div class="compText[^"]*"[^>]*>\s*<p[^>]*>(.*?)</p>`)
	reYahooRU      = regexp.MustCompile(`/RU=([^/]+)/`)
	reSpan         = regexp.MustCompile(`(?s)<span.*?</span>`)
)

// ParseYahooHTML reads Yahoo's result page: advertisements skipped, redirect links unwrapped, https only, each address once (pure; unit-tested).
func ParseYahooHTML(page string, n int) []SearchResult {
	var out []SearchResult
	seen := map[string]bool{}
	chunks := strings.Split(page, `<div class="dd algo`) // one chunk per result block (the first piece is what comes before the first result)
	if len(chunks) > 1 {
		chunks = chunks[1:]
	} else {
		chunks = nil
	}
	for _, block := range chunks {
		m := reYahooTitle.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		href := html.UnescapeString(m[1])
		if strings.Contains(href, "aclick") {
			continue
		}
		target := href
		if ru := reYahooRU.FindStringSubmatch(href); ru != nil {
			if dec, err := url.PathUnescape(ru[1]); err == nil {
				target = dec
			}
		}
		pu, err := url.Parse(target)
		if err != nil || pu.Scheme != "https" || pu.Host == "" || pu.User != nil || seen[target] {
			continue
		}
		title := m[2]
		if title == "" {
			title = reSpan.ReplaceAllString(m[3], "")
		}
		snippet := ""
		if s := reYahooSnippet.FindStringSubmatch(block); s != nil {
			snippet = cleanSnippet(s[1])
		}
		seen[target] = true
		out = append(out, SearchResult{Title: oneLineText(cleanSnippet(title), 300), URL: target, Snippet: oneLineText(snippet, 600)})
		if len(out) >= n {
			break
		}
	}
	return out
}

// pageClient is the plain HTTP client of the search pages: through the SSRF guard, HTTP/2 (Yahoo answers HTTP/1.1 clients with a 500), no redirects followed.
func pageClient(g *Guard, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DialContext: g.DialContext, ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// pageHeaders are the standard headers of a page request; the client says who it is (the same honest user agent as every other AURA request, no browser is claimed).
func pageHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en;q=0.6")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
}

// yahooClient follows Yahoo's own redirects (to its regional hosts, e.g. au.search.yahoo.com, and back with a cookie) and nowhere else.
func yahooClient(g *Guard, timeout time.Duration) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: timeout, Jar: jar, Transport: &http.Transport{Proxy: nil, DialContext: g.DialContext, ForceAttemptHTTP2: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 4 || req.URL.Scheme != "https" || !(req.URL.Hostname() == "search.yahoo.com" || strings.HasSuffix(req.URL.Hostname(), ".search.yahoo.com")) {
				return http.ErrUseLastResponse
			}
			pageHeaders(req)
			return nil
		}}
}

func (y *YahooHTML) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ { // Yahoo sometimes drops a connection: a few more tries with a pause, then give up
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, last
			case <-time.After(time.Duration(attempt) * 600 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://search.yahoo.com/search?"+url.Values{"p": {query}}.Encode(), nil)
		if err != nil {
			return nil, err
		}
		pageHeaders(req)
		resp, err := yahooClient(y.guard, y.timeout).Do(req)
		if err != nil {
			last = fmt.Errorf("%w: %v", ErrSearch, err)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			last = fmt.Errorf("%w: search.yahoo.com returned HTTP %d", ErrSearch, resp.StatusCode)
			continue
		}
		if rerr != nil {
			last = fmt.Errorf("%w: %v", ErrSearch, rerr)
			continue
		}
		return ParseYahooHTML(string(body), n), nil
	}
	return nil, last
}

// Merged asks every searcher at once and takes their results in turns, each address once; one failing costs nothing, all failing is an error.
type Merged []Searcher

func (m Merged) Name() string { return "web" }

func pageKey(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return u
	}
	return strings.TrimPrefix(strings.ToLower(pu.Host), "www.") + strings.TrimRight(pu.Path, "/")
}

func (m Merged) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	type answer struct {
		res []SearchResult
		err error
	}
	ch := make([]chan answer, len(m))
	for i, s := range m {
		ch[i] = make(chan answer, 1)
		go func(i int, s Searcher) {
			r, err := s.Search(ctx, query, n+3)
			ch[i] <- answer{r, err}
		}(i, s)
	}
	var lists [][]SearchResult
	var errs []string
	for i := range m {
		a := <-ch[i]
		if a.err != nil {
			errs = append(errs, m[i].Name()+": "+a.err.Error())
			continue
		}
		lists = append(lists, a.res)
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSearch, strings.Join(errs, "; "))
	}
	var out []SearchResult
	seen := map[string]bool{}
	for i := 0; len(out) < n; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) {
				added = true
				if k := pageKey(l[i].URL); !seen[k] && len(out) < n {
					seen[k] = true
					out = append(out, l[i])
				}
			}
		}
		if !added {
			break
		}
	}
	return out, nil
}
