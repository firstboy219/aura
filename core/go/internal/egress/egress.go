// Package egress is the browser's only way out (Blueprint 8.8.1 rule 2, P4,
// ADR-0039). The browser container sits on an internal Docker network with no
// route to the internet; Chromium is configured to use this proxy, which runs
// in the Core. Every connection — page, subresource, frame, WebSocket — is a
// CONNECT or absolute-URI request here and is checked per browser session:
// session credentials (Proxy-Authorization), the task's domain allowlist,
// ports 80/443 only, and the SSRF guard (DNS resolved once, every address
// checked, the checked IP dialled). A pre-navigation URL check alone is not
// relied upon.
package egress

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aura.local/core/internal/tools"
)

// Session is one browser session's egress policy.
type Session struct {
	ID          string
	Token       string
	TaskID      string
	AllowDomain []string
	// AnyPublic lets the session reach any public host (shell commands, ADR-0065); the SSRF guard still refuses
	// private, loopback, link-local and metadata addresses.
	AnyPublic bool
	Allowed   atomic.Int64
	Denied    atomic.Int64
	hostsMu   sync.Mutex
	hosts     map[string]int
}

// Hosts lists the hosts the session reached, with how often (at most 100 hosts).
func (s *Session) Hosts() map[string]int {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	out := make(map[string]int, len(s.hosts))
	for h, n := range s.hosts {
		out[h] = n
	}
	return out
}

func (s *Session) reached(host string) {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	if s.hosts == nil {
		s.hosts = map[string]int{}
	}
	if _, ok := s.hosts[host]; ok || len(s.hosts) < 100 {
		s.hosts[host]++
	}
}

// Denial is reported to the owner of a session (trace).
type Denial struct {
	SessionID, Host, Reason string
}

// Proxy authorizes and relays browser traffic.
type Proxy struct {
	guard    *tools.Guard
	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[string]*Session
	OnDeny   func(Denial) // optional: record denials in the task trace
	dialer   func(ctx context.Context, network, addr string) (net.Conn, error)
}

// New builds a proxy that dials through guard.
func New(guard *tools.Guard, log *slog.Logger) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	return &Proxy{guard: guard, log: log, sessions: map[string]*Session{}, dialer: guard.DialContext}
}

// Add registers a session; Remove ends it (its next connection is refused).
func (p *Proxy) Add(s *Session) {
	p.mu.Lock()
	p.sessions[s.ID] = s
	p.mu.Unlock()
}

func (p *Proxy) Remove(id string) {
	p.mu.Lock()
	delete(p.sessions, id)
	p.mu.Unlock()
}

// Session returns a registered session (nil if none).
func (p *Proxy) Session(id string) *Session {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[id]
}

// RemoveAll ends every session (kill switch).
func (p *Proxy) RemoveAll() {
	p.mu.Lock()
	p.sessions = map[string]*Session{}
	p.mu.Unlock()
}

func (p *Proxy) session(r *http.Request) *Session {
	h := r.Header.Get("Proxy-Authorization")
	raw, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	id, tok, ok := strings.Cut(string(b), ":")
	if !ok {
		return nil
	}
	p.mu.RLock()
	s := p.sessions[id]
	p.mu.RUnlock()
	if s == nil || subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
		return nil
	}
	return s
}

func (p *Proxy) deny(w http.ResponseWriter, s *Session, host, reason string, code int) {
	if s != nil {
		s.Denied.Add(1)
		if p.OnDeny != nil {
			p.OnDeny(Denial{SessionID: s.ID, Host: host, Reason: reason})
		}
	}
	w.Header().Set("X-Aura-Egress", "denied: "+reason)
	http.Error(w, "AURA egress policy: "+reason, code)
}

// check applies the session policy to host:port.
func check(s *Session, host, port string) string {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	if port != "443" && port != "80" {
		return "port " + port + " not allowed"
	}
	if !s.AnyPublic && !tools.DomainAllowed(h, s.AllowDomain) {
		return "domain " + h + " not in the task allowlist"
	}
	return ""
}

// ServeHTTP handles CONNECT tunnels and plain http:// proxy requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s := p.session(r)
	if s == nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="aura"`)
		http.Error(w, "AURA egress: unknown or ended browser session", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r, s)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		p.deny(w, s, r.URL.Host, "only http:// requests or CONNECT tunnels", http.StatusBadRequest)
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if port == "" {
		port = "80"
	}
	if why := check(s, host, port); why != "" {
		p.deny(w, s, host, why, http.StatusForbidden)
		return
	}
	tr := &http.Transport{DialContext: p.dialer, Proxy: nil, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	resp, err := tr.RoundTrip(out)
	if err != nil {
		p.deny(w, s, host, blockedReason(err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	s.Allowed.Add(1)
	s.reached(strings.ToLower(host))
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, 50<<20))
}

func blockedReason(err error) string {
	if errors.Is(err, tools.ErrBlockedAddress) {
		return "address blocked (private, loopback, link-local, metadata or reserved)"
	}
	return "connection failed"
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, s *Session) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		p.deny(w, s, r.Host, "bad CONNECT target", http.StatusBadRequest)
		return
	}
	if why := check(s, host, port); why != "" {
		p.deny(w, s, host, why, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	up, err := p.dialer(ctx, "tcp", net.JoinHostPort(host, port))
	cancel()
	if err != nil {
		p.deny(w, s, host, blockedReason(err), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	s.Allowed.Add(1)
	s.reached(strings.ToLower(host))
	down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		if buf.Reader.Buffered() > 0 {
			io.CopyN(up, buf, int64(buf.Reader.Buffered()))
		}
		io.Copy(up, down)
		up.Close()
	}()
	io.Copy(down, up)
	down.Close()
}

// Serve listens on addr until ctx ends.
func (p *Proxy) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	p.log.Info("browser egress proxy listening", "addr", addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
