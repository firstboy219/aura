// Package llmgate is AURA's model gateway for JARVIS (ADR-0083, owner 2026-11-18: "sesuai rencana di blueprint ODI": AURA holds the provider keys and
// is the only way out to an outside AI). JARVIS sends the provider request it already builds (chat completions, transcriptions, speech,
// embeddings…) to /api/v1/llm/{provider}/{path}; AURA checks the provider and path against a fixed allowlist, adds the key from its vault,
// forwards it, streams the answer back as it comes (voice must not wait), records the call (usage_records, egress log) and never logs content.
package llmgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aura.local/core/internal/egresslog"
)

// Upstream is one provider AURA may reach for JARVIS.
type Upstream struct {
	Base   string            // https://openrouter.ai/api/v1
	Scheme string            // "Bearer" or "Token" (Deepgram)
	Keys   map[string]string // key class ("chat", "latar", "") → vault ref; "" is the default
	Paths  []string          // allowed path prefixes after the provider, e.g. "chat/completions"
}

// Call is what is recorded for one forwarded request (sizes and outcome, never content).
type Call struct {
	Provider, Path, Model, Caller, Outcome string
	Status                                 int
	LatencyMS                              int64
	CostUSD                                float64
	BytesIn, BytesOut                      int64
}

// Gate forwards JARVIS's provider requests.
type Gate struct {
	Upstreams map[string]Upstream
	Secret    func(ctx context.Context, ref string) (string, error)
	Record    func(ctx context.Context, c Call)
	HTTP      *http.Client
}

const maxRequest = 26 << 20 // audio uploads; chat bodies are far smaller

// Default is the provider set JARVIS uses today, with the vault refs its keys were imported under.
func Default() map[string]Upstream {
	return map[string]Upstream{
		"openrouter": {Base: "https://openrouter.ai/api/v1", Scheme: "Bearer",
			Keys:  map[string]string{"": "vault:connector.jarvis_openrouter", "chat": "vault:connector.jarvis_openrouter_chat", "latar": "vault:connector.jarvis_openrouter_latar"},
			Paths: []string{"chat/completions", "embeddings", "audio/transcriptions", "audio/speech", "credits", "key", "models", "generation"}},
		"groq": {Base: "https://api.groq.com/openai/v1", Scheme: "Bearer", Keys: map[string]string{"": "vault:connector.jarvis_groq"},
			Paths: []string{"chat/completions", "audio/transcriptions", "models"}},
		"gemini": {Base: "https://generativelanguage.googleapis.com/v1beta/openai", Scheme: "Bearer", Keys: map[string]string{"": "vault:gemini"},
			Paths: []string{"chat/completions", "models", "embeddings"}},
		"deepgram": {Base: "https://api.deepgram.com", Scheme: "Token", Keys: map[string]string{"": "vault:connector.jarvis_deepgram"},
			Paths: []string{"v1/listen", "v1/auth/grant"}},
	}
}

func (g *Gate) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	// no overall timeout: a streamed answer may run long; the caller's request context ends it
	return &http.Client{Transport: egresslog.Wrap("jarvis-llm", nil), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func allowed(u Upstream, path string) bool {
	if strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	for _, p := range u.Paths {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

var costRe = regexp.MustCompile(`"cost"\s*:\s*([0-9.eE+-]+)`)

// ServeHTTP handles /api/v1/llm/{provider}/{path...}.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rc := http.NewResponseController(w) // a long streamed answer (a discussion, an illustration) outlives the server's 60 s write timeout
	_ = rc.SetWriteDeadline(start.Add(15 * time.Minute))
	_ = rc.SetReadDeadline(start.Add(5 * time.Minute))
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/llm/")
	provider, path, _ := strings.Cut(rest, "/")
	u, ok := g.Upstreams[provider]
	if !ok || !allowed(u, path) {
		writeErr(w, http.StatusForbidden, "not_allowed", "provider or path is not on AURA's model-gateway allowlist")
		return
	}
	class := r.Header.Get("X-Key-Class")
	ref := u.Keys[class]
	if ref == "" {
		ref = u.Keys[""]
	}
	key, err := g.Secret(r.Context(), ref)
	if (err != nil || key == "") && class != "" {
		key, err = g.Secret(r.Context(), u.Keys[""]) // no key for that class: the provider's default key
	}
	if err != nil || key == "" {
		writeErr(w, http.StatusServiceUnavailable, "no_key", "AURA holds no key for "+provider)
		return
	}
	var body io.Reader
	var model string
	var in int64
	if r.Body != nil {
		lim := http.MaxBytesReader(w, r.Body, maxRequest)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			raw, err := io.ReadAll(lim)
			if err != nil {
				writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "request too large")
				return
			}
			var probe struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(raw, &probe)
			model, in, body = probe.Model, int64(len(raw)), bytes.NewReader(raw)
		} else {
			body = lim
			in = r.ContentLength
		}
	}
	target := strings.TrimRight(u.Base, "/") + "/" + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad upstream request")
		return
	}
	for _, h := range []string{"Content-Type", "Accept", "X-Session-Id", "HTTP-Referer", "X-Title"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("Authorization", u.Scheme+" "+key)
	resp, err := g.client().Do(req)
	call := Call{Provider: provider, Path: path, Model: model, Caller: r.Header.Get("X-Caller"), BytesIn: in}
	if err != nil {
		call.Outcome, call.LatencyMS = "unreachable", time.Since(start).Milliseconds()
		g.record(r.Context(), call)
		code := http.StatusBadGateway
		if errors.Is(r.Context().Err(), context.Canceled) {
			code = 499
		}
		writeErr(w, code, "upstream_unreachable", provider+" unreachable")
		return
	}
	defer resp.Body.Close()
	for _, h := range []string{"Content-Type", "Cache-Control", "Retry-After", "X-Ratelimit-Remaining-Requests", "X-Ratelimit-Reset-Requests"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	fl, _ := w.(http.Flusher)
	tail := make([]byte, 0, 8192) // the last bytes: the usage chunk (with OpenRouter's cost) comes at the end of a stream
	buf := make([]byte, 16<<10)
	var out int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break // JARVIS hung up (he cut it off): stop reading, the upstream request ends with our context
			}
			out += int64(n)
			if fl != nil {
				fl.Flush()
			}
			tail = append(tail, buf[:n]...)
			if len(tail) > 8192 {
				tail = append(tail[:0], tail[len(tail)-8192:]...)
			}
		}
		if rerr != nil {
			break
		}
	}
	call.Status, call.BytesOut, call.LatencyMS = resp.StatusCode, out, time.Since(start).Milliseconds()
	call.Outcome = "ok"
	if resp.StatusCode >= 400 {
		call.Outcome = "http_" + strconv.Itoa(resp.StatusCode)
	}
	if m := costRe.FindAllSubmatch(tail, -1); len(m) > 0 {
		call.CostUSD, _ = strconv.ParseFloat(string(m[len(m)-1][1]), 64)
	}
	g.record(r.Context(), call)
}

func (g *Gate) record(ctx context.Context, c Call) {
	if g.Record != nil {
		g.Record(context.WithoutCancel(ctx), c)
	}
}

func writeErr(w http.ResponseWriter, code int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": kind, "message": msg}})
}
