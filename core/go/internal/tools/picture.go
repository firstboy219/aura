package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Picture is an image the Core found for a named person, organisation or
// thing, with the page it came from (shown as its source).
type Picture struct {
	ID          string // sha256 of the bytes
	ContentType string // image/jpeg | image/png | image/webp
	Bytes       []byte
	Title       string // the page title, as found
	SourceURL   string // the page the picture illustrates
}

// PictureFinder looks a name up and returns its picture, or nil when it has none.
type PictureFinder interface {
	Find(ctx context.Context, name string) (*Picture, error)
}

// MaxPictureBytes bounds a stored picture.
const MaxPictureBytes = 400 << 10

// WikiPictures finds the lead image of the best-matching Wikipedia page
// (ADR-0061). It asks only for freely licensed page images, talks only to
// fixed hosts through the SSRF guard, and never follows a redirect.
type WikiPictures struct {
	api        apiClient
	guard      *Guard
	imageHosts []string // the only hosts a picture may be fetched from
}

func NewWikiPictures(g *Guard, lang string) *WikiPictures {
	if lang == "" {
		lang = "en"
	}
	return &WikiPictures{api: apiClient{guard: g, host: lang + ".wikipedia.org", timeout: 8 * time.Second}, guard: g, imageHosts: []string{"upload.wikimedia.org", "thumb.wikimedia.org"}}
}

func (w *WikiPictures) Find(ctx context.Context, name string) (*Picture, error) {
	q := url.Values{"action": {"query"}, "format": {"json"}, "utf8": {"1"}, "generator": {"search"}, "gsrsearch": {name}, "gsrlimit": {"1"},
		"prop": {"pageimages|info"}, "inprop": {"url"}, "piprop": {"thumbnail"}, "pithumbsize": {"320"}, "pilicense": {"free"}, "redirects": {"1"}}
	var out struct {
		Query struct {
			Pages map[string]struct {
				Title     string `json:"title"`
				FullURL   string `json:"fullurl"`
				Thumbnail struct {
					Source string `json:"source"`
				} `json:"thumbnail"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := w.api.getJSON(ctx, "https://"+w.api.host+"/w/api.php?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	for _, p := range out.Query.Pages {
		if p.Thumbnail.Source == "" {
			return nil, nil
		}
		body, ct, err := w.image(ctx, p.Thumbnail.Source)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		src := p.FullURL
		if u, err := url.Parse(src); err != nil || u.Scheme != "https" || u.Hostname() != w.api.host {
			src = ""
		}
		return &Picture{ID: hex.EncodeToString(sum[:]), ContentType: ct, Bytes: body, Title: p.Title, SourceURL: src}, nil
	}
	return nil, nil
}

func (w *WikiPictures) image(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !slices.Contains(w.imageHosts, u.Hostname()) {
		return nil, "", fmt.Errorf("%w: picture is not on a Wikimedia image host", ErrSearch)
	}
	client := &http.Client{Timeout: w.api.timeout, Transport: &http.Transport{Proxy: nil, DialContext: w.guard.DialContext},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrSearch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("%w: %s returned HTTP %d", ErrSearch, u.Hostname(), resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPictureBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrSearch, err)
	}
	ct, ok := PictureType(body)
	if !ok || len(body) > MaxPictureBytes {
		return nil, "", fmt.Errorf("%w: not a small raster image", ErrSearch)
	}
	return body, ct, nil
}

// PictureType reports the raster type of the bytes themselves (never the
// header a server sent): JPEG, PNG or WebP. Anything else — SVG, HTML — is refused.
func PictureType(b []byte) (string, bool) {
	ct := http.DetectContentType(b)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch ct {
	case "image/jpeg", "image/png", "image/webp":
		return ct, true
	}
	return "", false
}
