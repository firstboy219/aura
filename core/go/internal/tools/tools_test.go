package tools

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBlockedMatrix(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0",
		"224.0.0.1", "255.255.255.255", "::1", "::", "fe80::1", "fc00::1", "fd12::1", "::ffff:127.0.0.1",
		"::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "64:ff9b::7f00:1", "198.18.0.1", "192.0.2.1"}
	for _, s := range blocked {
		if !Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s not blocked", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111", "64:ff9b::101:101"} {
		if Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s blocked", s)
		}
	}
}

func TestCheckURL(t *testing.T) {
	allow := []string{"example.com", "*.docs.example.org"}
	bad := []string{"ftp://example.com/", "file:///etc/passwd", "http://user:pw@example.com/", "http://example.com:8080/",
		"http://evil.com/", "http://example.com.evil.com/", "http://docs.example.org/", "gopher://example.com/"}
	for _, s := range bad {
		u, _ := url.Parse(s)
		if CheckURL(u, allow) == nil {
			t.Errorf("%s accepted", s)
		}
	}
	for _, s := range []string{"https://example.com/a", "http://example.com:80/", "https://a.docs.example.org/"} {
		u, _ := url.Parse(s)
		if err := CheckURL(u, allow); err != nil {
			t.Errorf("%s rejected: %v", s, err)
		}
	}
}

type fakeResolver struct {
	answers map[string][]netip.Addr
	calls   atomic.Int32
}

func (f *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.calls.Add(1)
	if a, ok := f.answers[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// A hostname resolving to a private address, or to a mix of public and
// private addresses, is refused before any connection is made.
func TestDialerRejectsPrivateResolution(t *testing.T) {
	r := &fakeResolver{answers: map[string][]netip.Addr{
		"internal.example.com": {netip.MustParseAddr("10.0.0.5")},
		"mixed.example.com":    {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
		"meta.example.com":     {netip.MustParseAddr("169.254.169.254")},
		"v6.example.com":       {netip.MustParseAddr("::ffff:10.0.0.1")},
	}}
	f := NewFetcher(&Guard{Resolver: r}, 1<<20, 2*time.Second)
	for host := range r.answers {
		_, err := f.Fetch(context.Background(), "http://"+host+"/", []string{host})
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%s: %v", host, err)
		}
	}
	_, err := f.Fetch(context.Background(), "http://127.0.0.1/", []string{"127.0.0.1"})
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("IP literal: %v", err)
	}
}

// DNS rebinding: the dialer connects only to the address it checked in the
// same resolution, so a second answer cannot be used.
func TestDNSChangeBetweenResolutions(t *testing.T) {
	var n atomic.Int32
	r := resolverFunc(func(host string) []netip.Addr {
		if n.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	})
	g := &Guard{Resolver: r}
	// First dial: public answer (connection may fail, but it is never to 127.0.0.1).
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	g.DialContext(ctx, "tcp", "rebind.example.com:80")
	// Second dial: private answer is rejected.
	if _, err := g.DialContext(context.Background(), "tcp", "rebind.example.com:80"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("second resolution not checked: %v", err)
	}
}

type resolverFunc func(string) []netip.Addr

func (f resolverFunc) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return f(host), nil
}

func fixture(t *testing.T, h http.HandlerFunc) (*httptest.Server, string) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return srv, u.Host
}

// Fixture servers listen on 127.0.0.1:<random>; tests reach them through
// names mapped to loopback with a test guard whose dials go to the fixture port.
func testFetcher(t *testing.T, hostport string, max int64, timeout time.Duration) *Fetcher {
	_, port, _ := net.SplitHostPort(hostport)
	g := NewTestGuard(resolverFunc(func(string) []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }))
	g.portOverride = port
	return NewFetcher(g, max, timeout)
}

func TestFetchHTMLAndLimits(t *testing.T) {
	_, hp := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>T &amp; X</title><script>evil()</script></head><body><p>Hello</p><p>World</p></body></html>`))
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(strings.Repeat("a", 5000)))
		case "/bin":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("x"))
		case "/slow":
			time.Sleep(2 * time.Second)
		case "/redir-out":
			http.Redirect(w, r, "http://evil.com/", http.StatusFound)
		case "/redir-meta":
			http.Redirect(w, r, "http://meta.fixture.test/", http.StatusFound)
		}
	})
	f := testFetcher(t, hp, 1000, 500*time.Millisecond)
	allow := []string{"fixture.test", "meta.fixture.test"}
	res, err := f.Fetch(context.Background(), "http://fixture.test/page", allow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "T & X" || strings.Contains(res.Text, "evil") || !strings.Contains(res.Text, "Hello") || res.SHA256 == "" {
		t.Fatalf("%+v", res)
	}
	if _, err := f.Fetch(context.Background(), "http://fixture.test/big", allow); !errors.Is(err, ErrTooLarge) {
		t.Errorf("size cap: %v", err)
	}
	if _, err := f.Fetch(context.Background(), "http://fixture.test/bin", allow); !errors.Is(err, ErrContentType) {
		t.Errorf("content type: %v", err)
	}
	if _, err := f.Fetch(context.Background(), "http://fixture.test/slow", allow); err == nil {
		t.Error("timeout not enforced")
	}
	if _, err := f.Fetch(context.Background(), "http://fixture.test/redir-out", allow); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("redirect to non-allowlisted domain: %v", err)
	}
}

func TestRedirectToPrivateAddressBlocked(t *testing.T) {
	_, hp := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://meta.example.com/latest/meta-data/", http.StatusFound)
	})
	_, port, _ := net.SplitHostPort(hp)
	r := resolverFunc(func(host string) []netip.Addr {
		if host == "meta.example.com" {
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	})
	g := NewTestGuard(r)
	g.portOverride = port
	f := NewFetcher(g, 1000, time.Second)
	_, err := f.Fetch(context.Background(), "http://start.example.com/", []string{"start.example.com", "meta.example.com"})
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("redirect to metadata IP: %v", err)
	}
}

func TestProxyEnvironmentIgnored(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyHits.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	_, hp := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("direct"))
	})
	f := testFetcher(t, hp, 1000, time.Second)
	res, err := f.Fetch(context.Background(), "http://fixture.test/", []string{"fixture.test"})
	if err != nil || res.Text != "direct" || proxyHits.Load() != 0 {
		t.Fatalf("%v %+v proxy=%d", err, res, proxyHits.Load())
	}
}

func ws(t *testing.T) *Workspace {
	w, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestPathValidation(t *testing.T) {
	bad := []string{"", "/etc/passwd", "../x", "a/../../x", "a/..", "a\\b", "a\x00b", strings.Repeat("a/", 9) + "f", strings.Repeat("x", 300)}
	for _, p := range bad {
		if _, err := CleanPath(p); !errors.Is(err, ErrBadPath) {
			t.Errorf("%q accepted", p)
		}
	}
	if c, err := CleanPath("reports/./summary.md"); err != nil || c != "reports/summary.md" {
		t.Fatalf("%q %v", c, err)
	}
}

func TestWriteAtomicIdempotentAndRead(t *testing.T) {
	w := ws(t)
	r1, err := w.Write("T1", "reports/summary.md", "hello")
	if err != nil || r1.Skipped {
		t.Fatalf("%v %+v", err, r1)
	}
	r2, _ := w.Write("T1", "reports/summary.md", "hello")
	if !r2.Skipped || r2.SHA256 != r1.SHA256 {
		t.Fatal("same content must be skipped")
	}
	r3, _ := w.Write("T1", "reports/summary.md", "changed")
	if r3.Skipped {
		t.Fatal("changed content skipped")
	}
	got, err := w.Read("T1", "reports/summary.md")
	if err != nil || got.Content != "changed" {
		t.Fatalf("%v %+v", err, got)
	}
	entries, _ := os.ReadDir(filepath.Join(w.Root(), "T1", "reports"))
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
	if _, err := w.Read("T2", "reports/summary.md"); err == nil {
		t.Fatal("task workspaces are not isolated")
	}
}

func TestSymlinkEscapesRejected(t *testing.T) {
	w := ws(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600)
	w.Write("T1", "x.txt", "x")
	td := filepath.Join(w.Root(), "T1")
	os.Symlink(filepath.Join(outside, "secret"), filepath.Join(td, "link-file"))
	os.Symlink(outside, filepath.Join(td, "link-dir"))
	os.Symlink("x.txt", filepath.Join(td, "inner-link"))
	for _, p := range []string{"link-file", "link-dir/secret", "inner-link"} {
		if _, err := w.Read("T1", p); err == nil {
			t.Errorf("read through %s allowed", p)
		}
		if _, err := w.Write("T1", p, "pwn"); err == nil {
			t.Errorf("write through %s allowed", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "s" {
		t.Fatal("file outside workspace modified")
	}
	if _, err := w.Write("../escape", "a", "b"); err == nil {
		t.Fatal("task id traversal allowed")
	}
}
