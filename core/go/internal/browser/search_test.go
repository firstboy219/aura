package browser

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"aura.local/core/internal/egress"
	"aura.local/core/internal/tools"
)

func TestResultURLUnwrapsEngineLinksAndDropsUnsafeOnes(t *testing.T) {
	bing := "https://www.bing.com/ck/a?!&&p=abc&u=a1" + base64.RawURLEncoding.EncodeToString([]byte("https://news.detik.com/berita/1")) + "&ntb=1"
	cases := map[string]string{
		"https://www.kompas.com/x#top": "https://www.kompas.com/x",
		bing:                           "https://news.detik.com/berita/1",
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.antaranews.com%2Fa&rut=1": "https://www.antaranews.com/a",
		"javascript:alert(1)":            "",
		"https://user:pw@evil.example/":  "",
		"/relative/path":                 "",
		"file:///etc/passwd":             "",
		"https://www.bing.com/ck/a?u=zz": "",
	}
	for in, want := range cases {
		if got := ResultURL(in); got != want {
			t.Errorf("ResultURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeHost answers the browser commands a search uses and records them.
type fakeHost struct {
	mu      sync.Mutex
	methods []string
	urls    []string
	opened  []string // session ids
	live    func(id string) bool
	alive   bool                           // the egress session existed while the page was read
	results map[string][]map[string]string // by engine
	blocked bool
}

func (f *fakeHost) serve(t *testing.T, sock string) {
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rd := bufio.NewReader(c)
				for {
					line, err := rd.ReadBytes('\n')
					if err != nil {
						return
					}
					var m struct {
						ID     string         `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					json.Unmarshal(line, &m)
					f.mu.Lock()
					f.methods = append(f.methods, m.Method)
					res := map[string]any{"url": "", "title": "", "site_blocked": false}
					switch m.Method {
					case "session.open":
						f.opened = append(f.opened, m.Params["session_id"].(string))
					case "page.goto":
						f.urls = append(f.urls, m.Params["url"].(string))
					case "page.serp":
						res["results"] = f.results[m.Params["engine"].(string)]
						res["site_blocked"] = f.blocked
						if f.live != nil {
							f.alive = f.live(m.Params["session_id"].(string))
						}
					}
					f.mu.Unlock()
					b, _ := json.Marshal(map[string]any{"id": m.ID, "result": res})
					c.Write(append(b, '\n'))
				}
			}(c)
		}
	}()
}

type fixedSearcher struct{ called bool }

func (f *fixedSearcher) Name() string { return "fixed" }
func (f *fixedSearcher) Search(context.Context, string, int) ([]tools.SearchResult, error) {
	f.called = true
	return []tools.SearchResult{{Title: "Wiki", URL: "https://en.wikipedia.org/wiki/X"}}, nil
}

func newTestSearcher(t *testing.T, f *fakeHost, fb tools.Searcher) (*Searcher, *egress.Proxy) {
	sock := filepath.Join(t.TempDir(), "b.sock")
	f.serve(t, sock)
	p := egress.New(tools.NewGuard(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	return NewSearcher(New(sock), p, []string{"bing", "duckduckgo"}, fb), p
}

func TestChromiumSearchReadsResultsAndClosesItsSession(t *testing.T) {
	f := &fakeHost{results: map[string][]map[string]string{"bing": {
		{"title": "  Berita\n terkini ", "href": "https://www.kompas.com/a", "snippet": "isi"},
		{"title": "Bing sendiri", "href": "https://www.bing.com/videos?q=x"},
		{"title": "duplikat", "href": "https://www.kompas.com/a"},
		{"title": "detik", "href": "https://news.detik.com/b"},
	}}}
	s, p := newTestSearcher(t, f, nil)
	f.live = func(id string) bool {
		es := p.Session(id)
		return es != nil && len(es.AllowDomain) > 0 && tools.DomainAllowed("www.bing.com", es.AllowDomain) && !tools.DomainAllowed("www.kompas.com", es.AllowDomain)
	}
	res, err := s.Search(context.Background(), "berita hari ini", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Title != "Berita terkini" || res[0].URL != "https://www.kompas.com/a" || res[1].URL != "https://news.detik.com/b" {
		t.Fatalf("results: %+v", res)
	}
	if f.urls[0] != "https://www.bing.com/search?q=berita+hari+ini" {
		t.Fatalf("engine URL: %s", f.urls[0])
	}
	want := []string{"session.open", "page.goto", "page.serp", "session.close"}
	if len(f.methods) != len(want) {
		t.Fatalf("calls: %v", f.methods)
	}
	for i := range want {
		if f.methods[i] != want[i] {
			t.Fatalf("calls: %v", f.methods)
		}
	}
	// The egress session reached only the engine, and existed only during the search.
	if !f.alive || p.Session(f.opened[0]) != nil {
		t.Fatalf("egress session: alive=%v left=%v", f.alive, p.Session(f.opened[0]) != nil)
	}
}

func TestChromiumSearchFallsBackWhenEnginesRefuse(t *testing.T) {
	f := &fakeHost{blocked: true, results: map[string][]map[string]string{"bing": {{"title": "x", "href": "https://a.example/"}}}}
	fb := &fixedSearcher{}
	s, _ := newTestSearcher(t, f, fb)
	res, err := s.Search(context.Background(), "q", 5)
	if err != nil || !fb.called || res[0].URL != "https://en.wikipedia.org/wiki/X" {
		t.Fatalf("fallback not used: %v %+v", err, res)
	}
	s.fallback = nil
	if _, err := s.Search(context.Background(), "q", 5); !errors.Is(err, tools.ErrSearch) {
		t.Fatalf("want ErrSearch, got %v", err)
	}
}
