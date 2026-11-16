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
	reYahooBlock   = regexp.MustCompile(`(?s)<div class="dd algo[^"]*"[^>]*>(.*?)(?:<div class="dd (?:algo|[a-zA-Z]+ )|$)`)
	reYahooTitle   = regexp.MustCompile(`(?s)<h3[^>]*class="title"[^>]*>\s*<a[^>]*href="([^"]+)"[^>]*?(?:aria-label="([^"]*)")?[^>]*>(.*?)</a>`)
	reYahooSnippet = regexp.MustCompile(`(?s)<div class="compText[^"]*"[^>]*>\s*<p[^>]*>(.*?)</p>`)
	reYahooRU      = regexp.MustCompile(`/RU=([^/]+)/`)
	reSpan         = regexp.MustCompile(`(?s)<span.*?</span>`)
)

// ParseYahooHTML reads Yahoo's result page: advertisements skipped, redirect links unwrapped, https only, each address once (pure; unit-tested).
func ParseYahooHTML(page string, n int) []SearchResult {
	var out []SearchResult
	seen := map[string]bool{}
	for _, b := range reYahooBlock.FindAllStringSubmatch(page, -1) {
		block := b[1]
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

func (y *YahooHTML) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	client := &http.Client{Timeout: y.timeout, Transport: &http.Transport{Proxy: nil, DialContext: y.guard.DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://search.yahoo.com/search?"+url.Values{"p": {query}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130 Safari/537.36")
	req.Header.Set("Accept-Language", "id-ID,id;q=0.9,en;q=0.6")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSearch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: search.yahoo.com returned HTTP %d", ErrSearch, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSearch, err)
	}
	return ParseYahooHTML(string(body), n), nil
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
