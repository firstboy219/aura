package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrTooLarge = errors.New("fetch_too_large")
var ErrContentType = errors.New("fetch_unsupported_content_type")

// FetchResult is what web.fetch returns. Text is untrusted content.
type FetchResult struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Title       string `json:"title"`
	Text        string `json:"text"`
}

// Fetcher implements web.fetch.
type Fetcher struct {
	guard    *Guard
	maxBytes int64
	timeout  time.Duration
}

func NewFetcher(g *Guard, maxBytes int64, timeout time.Duration) *Fetcher {
	return &Fetcher{guard: g, maxBytes: maxBytes, timeout: timeout}
}

const maxRedirects = 5

// Fetch GETs an allowlisted URL. Every redirect is re-validated against the
// allowlist and every connection goes through the guard's checked dialer.
// Proxy environment variables are ignored.
func (f *Fetcher) Fetch(ctx context.Context, raw string, allow []string) (*FetchResult, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlockedURL, err)
	}
	if err := CheckURL(u, allow); err != nil {
		return nil, err
	}
	client := &http.Client{
		Timeout: f.timeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            f.guard.DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  f.timeout,
			MaxResponseHeaderBytes: 64 << 10,
			DisableCompression:     false,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: too many redirects", ErrBlockedURL)
			}
			return CheckURL(r.URL, allow)
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "AURA/0.1 (+web.fetch)")
	req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !textual(ct) {
		return nil, fmt.Errorf("%w: %q", ErrContentType, ct)
	}
	if resp.ContentLength > f.maxBytes {
		return nil, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > f.maxBytes {
		return nil, ErrTooLarge
	}
	sum := sha256.Sum256(body)
	res := &FetchResult{URL: raw, FinalURL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: ct,
		Bytes: len(body), SHA256: hex.EncodeToString(sum[:])}
	if ct == "text/html" || ct == "application/xhtml+xml" {
		res.Title, res.Text = HTMLToText(string(body))
	} else {
		res.Text = string(body)
	}
	return res, nil
}

func textual(ct string) bool {
	return strings.HasPrefix(ct, "text/") || ct == "application/json" || ct == "application/xhtml+xml" || ct == "application/xml"
}

var (
	reDrop  = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg|head)\b.*?</(script|style|noscript|template|svg|head)\s*>`)
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reBlock = regexp.MustCompile(`(?i)</?(p|div|br|li|h[1-6]|tr|section|article|header|footer|ul|ol|table)\b[^>]*>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines = regexp.MustCompile(`\n\s*\n+`)
)

// HTMLToText is a small, dependency-free text extractor. Hidden elements are
// not specially treated: all page text is untrusted data either way.
func HTMLToText(s string) (title, text string) {
	if m := reTitle.FindStringSubmatch(s); m != nil {
		title = strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(m[1], "")))
	}
	s = reDrop.ReplaceAllString(s, " ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = reSpace.ReplaceAllString(s, " ")
	s = reLines.ReplaceAllString(s, "\n\n")
	return title, strings.TrimSpace(s)
}
