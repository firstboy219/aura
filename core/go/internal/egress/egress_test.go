package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/tools"
)

type mapResolver map[string]string

func (m mapResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if ip, ok := m[host]; ok {
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func setup(t *testing.T) (*Proxy, string, *[]Denial) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello from "+r.Host) }))
	t.Cleanup(upstream.Close)
	u, _ := url.Parse(upstream.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	g := tools.NewTestGuard(mapResolver{"allowed.test": "127.0.0.1", "rebind.test": "10.0.0.7", "meta.test": "169.254.169.254", "other.test": "127.0.0.1"})
	tools.SetTestPort(g, port)
	p := New(g, nil)
	var mu sync.Mutex
	var denials []Denial
	p.OnDeny = func(d Denial) { mu.Lock(); denials = append(denials, d); mu.Unlock() }
	p.Add(&Session{ID: "s1", Token: "tok-1", TaskID: "T1", AllowDomain: []string{"allowed.test", "rebind.test", "meta.test", "169.254.169.254"}})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, strings.TrimPrefix(srv.URL, "http://"), &denials
}

func auth(id, tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+tok))
}

// connect sends a CONNECT and returns the status code.
func connect(t *testing.T, proxy, target, creds string) int {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if creds != "" {
		req += "Proxy-Authorization: " + creds + "\r\n"
	}
	io.WriteString(c, req+"\r\n")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == 200 { // the tunnel works: speak HTTP through it
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: allowed.test\r\nConnection: close\r\n\r\n")
		inner, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil || inner.StatusCode != 200 {
			t.Fatalf("tunnel did not carry traffic: %v", err)
		}
	}
	return resp.StatusCode
}

func TestEgressPolicy(t *testing.T) {
	p, proxy, denials := setup(t)
	good := auth("s1", "tok-1")
	for name, tc := range map[string]struct {
		target, creds string
		want          int
	}{
		"allowed domain tunnel":             {"allowed.test:443", good, 200},
		"no credentials":                    {"allowed.test:443", "", 407},
		"wrong token":                       {"allowed.test:443", auth("s1", "nope"), 407},
		"unknown session":                   {"allowed.test:443", auth("s9", "tok-1"), 407},
		"domain not in allowlist":           {"other.test:443", good, 403},
		"port not allowed":                  {"allowed.test:22", good, 403},
		"allowed name resolving private":    {"rebind.test:443", good, 502},
		"allowed name resolving metadata":   {"meta.test:80", good, 502},
		"metadata IP literal (even listed)": {"169.254.169.254:80", good, 502},
	} {
		if got := connect(t, proxy, tc.target, tc.creds); got != tc.want {
			t.Fatalf("%s: status %d, want %d", name, got, tc.want)
		}
	}
	if len(*denials) < 5 {
		t.Fatalf("denials not reported: %v", *denials)
	}
	// An ended session cannot open anything.
	p.Remove("s1")
	if got := connect(t, proxy, "allowed.test:443", good); got != 407 {
		t.Fatalf("ended session: %d", got)
	}
}

func TestPlainHTTPThroughProxy(t *testing.T) {
	_, proxy, _ := setup(t)
	get := func(target string) (int, string) {
		pu, _ := url.Parse("http://" + proxy)
		tr := &http.Transport{Proxy: http.ProxyURL(pu), ProxyConnectHeader: http.Header{}}
		req, _ := http.NewRequest("GET", target, nil)
		req.Header.Set("Proxy-Authorization", auth("s1", "tok-1"))
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("http://allowed.test/page"); code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("allowed: %d %q", code, body)
	}
	if code, _ := get("http://other.test/x"); code != 403 {
		t.Fatalf("disallowed: %d", code)
	}
	if code, _ := get("http://rebind.test/x"); code != 502 {
		t.Fatalf("private target: %d", code)
	}
}
