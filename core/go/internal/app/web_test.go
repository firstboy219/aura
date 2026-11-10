package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/api"
	"aura.local/core/internal/config"
	"aura.local/core/internal/engine"
	"aura.local/core/internal/vault"
	"aura.local/core/internal/webauth"
)

const webHost = "aura.test.example"

type webEnv struct {
	*env
	cookie string
	csrf   string
	secret string
	last   *http.Cookie // the session cookie as set by the last successful login
}

func newWebEnv(t *testing.T) *webEnv {
	t.Helper()
	e := newEnvWith(t, 1000, func(cfg *config.Config, dir string) {
		key := filepath.Join(dir, "master.key")
		if _, err := vault.GenerateKeyFile(key); err != nil {
			t.Fatal(err)
		}
		cfg.Secrets.MasterKeyFile = key
		cfg.Web.Enabled, cfg.Web.Origin, cfg.Web.APIHosts = true, "https://"+webHost, []string{"api.test.example"}
	})
	return &webEnv{env: e}
}

// do sends a request with an explicit Host and optional cookie/CSRF/Origin.
func (w *webEnv) do(method, host, path, body string, hdr map[string]string) (*http.Response, string) {
	w.t.Helper()
	r, _ := http.NewRequest(method, w.srv.URL+path, strings.NewReader(body))
	r.Host = host
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// setUp runs the one-time setup link flow and signs in.
func (w *webEnv) setUp(t *testing.T) {
	t.Helper()
	link, err := api.CreateSetupLink(context.Background(), w.a.Store, "https://"+webHost, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token := link[strings.Index(link, "#/setup/")+len("#/setup/"):]
	resp, body := w.do("POST", webHost, "/api/v1/web/setup/start", `{"token":"`+token+`","password":"a long owner password"}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, "otpauth://") {
		t.Fatalf("setup start: %d %s", resp.StatusCode, body)
	}
	w.secret = body[strings.Index(body, `"secret":"`)+10:]
	w.secret = w.secret[:strings.Index(w.secret, `"`)]
	code, _ := webauth.TOTPCode(w.secret, time.Now())
	if resp, body := w.do("POST", webHost, "/api/v1/web/setup/confirm", `{"token":"`+token+`","code":"`+code+`"}`, nil); resp.StatusCode != 200 {
		t.Fatalf("setup confirm: %d %s", resp.StatusCode, body)
	}
	// The link works once.
	if resp, _ := w.do("POST", webHost, "/api/v1/web/setup/start", `{"token":"`+token+`","password":"another long password"}`, nil); resp.StatusCode != 401 {
		t.Fatalf("setup link reused: %d", resp.StatusCode)
	}
	w.login(t, "a long owner password", 1)
}

// login uses the code of step now+offset (each code works once).
func (w *webEnv) login(t *testing.T, pw string, offset int) *http.Response {
	t.Helper()
	code, _ := webauth.TOTPCode(w.secret, time.Now().Add(time.Duration(offset)*30*time.Second))
	resp, body := w.do("POST", webHost, "/api/v1/web/login", `{"password":"`+pw+`","code":"`+code+`"}`, nil)
	if resp.StatusCode == 200 {
		for _, c := range resp.Cookies() {
			if c.Name == "__Host-aura_session" {
				w.cookie, w.last = c.Value, c
			}
		}
		w.csrf = body[strings.Index(body, `"csrf":"`)+8:]
		w.csrf = w.csrf[:strings.Index(w.csrf, `"`)]
	}
	return resp
}

func (w *webEnv) session() map[string]string {
	return map[string]string{"Cookie": "__Host-aura_session=" + w.cookie}
}

// D.1 P5 web security tests: cookie flags, CSRF, origin, host separation,
// headers, step-up, logout, lockout.
func TestWebSecurity(t *testing.T) {
	w := newWebEnv(t)
	w.setUp(t)
	// Cookie flags (each authenticator code works once, so the setup's login is the one checked).
	c := w.last
	if c == nil || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("session cookie flags: %+v", c)
	}
	task := `{"objective":"Fetch https://example.com/ and write reports/a.md","tools":["web.fetch","workspace.write"],"allow_domains":["example.com"]}`
	// Reading with the cookie works; changing without CSRF does not.
	if resp, _ := w.do("GET", webHost, "/api/v1/tasks", "", w.session()); resp.StatusCode != 200 {
		t.Fatalf("GET with session: %d", resp.StatusCode)
	}
	if resp, _ := w.do("POST", webHost, "/api/v1/tasks", task, w.session()); resp.StatusCode != 401 {
		t.Fatalf("POST without CSRF: %d", resp.StatusCode)
	}
	bad := w.session()
	bad["X-CSRF-Token"] = "nope"
	if resp, _ := w.do("POST", webHost, "/api/v1/tasks", task, bad); resp.StatusCode != 401 {
		t.Fatalf("POST with a wrong CSRF token: %d", resp.StatusCode)
	}
	cross := w.session()
	cross["X-CSRF-Token"], cross["Origin"] = w.csrf, "https://evil.example"
	if resp, _ := w.do("POST", webHost, "/api/v1/tasks", task, cross); resp.StatusCode != 401 {
		t.Fatalf("cross-origin POST: %d", resp.StatusCode)
	}
	good := w.session()
	good["X-CSRF-Token"], good["Origin"] = w.csrf, "https://"+webHost
	if resp, body := w.do("POST", webHost, "/api/v1/tasks", task, good); resp.StatusCode != 201 {
		t.Fatalf("POST with session and CSRF: %d %s", resp.StatusCode, body)
	}
	// The session cookie counts only on the web host; the API host takes bearer tokens only.
	if resp, _ := w.do("GET", "api.test.example", "/api/v1/tasks", "", w.session()); resp.StatusCode != 401 {
		t.Fatalf("cookie accepted on the API host: %d", resp.StatusCode)
	}
	if resp, _ := w.do("GET", "api.test.example", "/api/v1/tasks", "", map[string]string{"Authorization": "Bearer " + w.token}); resp.StatusCode != 200 {
		t.Fatalf("bearer on the API host: %d", resp.StatusCode)
	}
	if resp, _ := w.do("GET", "api.test.example", "/app/", "", nil); resp.StatusCode != 404 {
		t.Fatalf("app served on the API host: %d", resp.StatusCode)
	}
	// App headers.
	resp, body := w.do("GET", webHost, "/app/", "", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") ||
		!strings.Contains(csp, "frame-ancestors 'none'") || resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(body, "/app/app.js") {
		t.Fatalf("app headers: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ := w.do("GET", webHost, "/", "", nil); resp.StatusCode != 302 && resp.StatusCode != 200 {
		t.Fatalf("root on the web host: %d", resp.StatusCode)
	}
	// Approving from the web needs a fresh authenticator code (step-up); rejecting does not.
	ap := w.session()
	ap["X-CSRF-Token"] = w.csrf
	if resp, body := w.do("POST", webHost, "/api/v1/approvals/01APPROVALAAAAAAAAAAAAAAAA/approve", `{"action_hash":"`+strings.Repeat("a", 64)+`"}`, ap); resp.StatusCode != 401 ||
		!strings.Contains(body, "step_up_required") {
		t.Fatalf("web approval without step-up: %d %s", resp.StatusCode, body)
	}
	// Logout ends the session server-side.
	w.do("POST", webHost, "/api/v1/web/logout", "", ap)
	if resp, _ := w.do("GET", webHost, "/api/v1/tasks", "", w.session()); resp.StatusCode != 401 {
		t.Fatalf("session still valid after logout: %d", resp.StatusCode)
	}
	// Unauthenticated: denied everywhere (hiding a UI element is never access control).
	for _, p := range []string{"/api/v1/tasks", "/api/v1/approvals", "/api/v1/tools", "/api/v1/self", "/api/v1/browser/profiles"} {
		if resp, _ := w.do("GET", webHost, p, "", nil); resp.StatusCode != 401 {
			t.Fatalf("%s without credentials: %d", p, resp.StatusCode)
		}
	}
}

func TestWebLoginLockoutAndReplay(t *testing.T) {
	w := newWebEnv(t)
	w.setUp(t)
	// A used code does not work again.
	if resp := w.login(t, "a long owner password", 1); resp.StatusCode != 401 {
		t.Fatalf("replayed code accepted: %d", resp.StatusCode)
	}
	for i := 0; i < 4; i++ {
		if resp := w.login(t, "wrong password!!", 0); resp.StatusCode != 401 {
			t.Fatalf("wrong password: %d", resp.StatusCode)
		}
	}
	// Five failures lock the account, even for the right password and a fresh code.
	if resp := w.login(t, "a long owner password", 0); resp.StatusCode != 423 {
		t.Fatalf("not locked after repeated failures: %d", resp.StatusCode)
	}
	if resp, _ := w.do("POST", "localhost", "/api/v1/web/login", `{"password":"x","code":"000000"}`, nil); resp.StatusCode != 404 {
		t.Fatalf("login served outside the web host: %d", resp.StatusCode)
	}
}

func TestWebFilesNeverActiveContent(t *testing.T) {
	w := newWebEnv(t)
	w.setUp(t)
	ws := w.a.Cfg.Workspace.Root
	tk, _, err := w.a.Engine.Submit(context.Background(), engine.CreateInput{Objective: "Fetch https://example.com/ and write reports/a.md",
		Tools: []string{"web.fetch", "workspace.write"}, AllowDomains: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	id := tk.ID
	os.MkdirAll(filepath.Join(ws, id, "downloads"), 0o755)
	os.WriteFile(filepath.Join(ws, id, "downloads", "x.html"), []byte("<script>alert(1)</script>"), 0o644)
	resp, _ := w.do("GET", webHost, "/api/v1/tasks/"+id+"/files/downloads/x.html", "", w.session())
	if resp.StatusCode != 200 {
		t.Fatalf("file not served (status %d): the check below would prove nothing", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("a workspace file is served as active content: %s %v", ct, resp.Header)
	}
}
