package nexus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/gateway"
	"aura.local/core/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// fake is a NEXUS stand-in: ready and submit are set per test.
type fake struct {
	mu       sync.Mutex
	ready    int
	submit   func(w http.ResponseWriter, r Request)
	requests int
}

func (f *fake) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		code := f.ready
		f.mu.Unlock()
		w.WriteHeader(code)
	})
	mux.HandleFunc("POST /api/v1/cognitive/requests", func(w http.ResponseWriter, r *http.Request) {
		var req Request
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.requests++
		fn := f.submit
		f.mu.Unlock()
		fn(w, req)
	})
	mux.HandleFunc("POST /api/v1/cognitive/requests/{id}/cancel", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) })
	return mux
}

func (f *fake) count() int     { f.mu.Lock(); defer f.mu.Unlock(); return f.requests }
func (f *fake) setReady(c int) { f.mu.Lock(); f.ready = c; f.mu.Unlock() }

func ok(content string) func(http.ResponseWriter, Request) {
	return func(w http.ResponseWriter, r Request) {
		writeResult(w, 200, r, "SUCCEEDED", content, "low")
	}
}

func writeResult(w http.ResponseWriter, code int, r Request, status, content, band string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	res := map[string]any{"request_id": r.RequestID, "status": status, "result": map[string]any{"content": content, "limitations": []string{"missing evidence"}},
		"confidence": map[string]any{"band": band, "method": "none"}, "execution_authority": "none", "model": map[string]any{"model_id": "nexus-mini", "version": "0.1"},
		"trace_id": "t", "created_at": "2026-11-16T00:00:00Z", "schema_version": "1.0"}
	json.NewEncoder(w).Encode(res)
}

func errEnv(w http.ResponseWriter, code int, c string) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": c, "message": c, "retryable": false, "request_id": "r", "trace_id": "t", "schema_version": "1.0"}})
}

type rig struct {
	t      *testing.T
	st     *store.Store
	f      *fake
	srv    *httptest.Server
	clk    *clock
	svc    *Service
	legacy int
	last   gateway.Request
	lastB  gateway.Budget
}

func newRig(t *testing.T, mutate func(*Policy)) *rig {
	t.Helper()
	d := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, f: &fake{ready: 200, submit: ok("from nexus")}, clk: &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}}
	r.srv = httptest.NewServer(r.f.handler())
	t.Cleanup(r.srv.Close)
	legacy := func(ctx context.Context, b gateway.Budget, req gateway.Request) (*gateway.Result, error) {
		r.legacy++
		r.last, r.lastB = req, b
		return &gateway.Result{Content: "from legacy", Provider: "gemini", RouteID: "g", APIModelID: "g1"}, nil
	}
	r.svc = NewService(st, legacy, func() string { return "tok" }, "aura-core", "w1", MonitorConfig{FailThreshold: 3, FailWindow: time.Minute, RecoverSuccesses: 2, HoldDown: 30 * time.Second, StaleAfter: 45 * time.Second})
	r.svc.Mon.now = r.clk.Now
	r.svc.Auth.Now, r.svc.Router.Now, r.svc.Teacher.Now = r.clk.Now, r.clk.Now, r.clk.Now
	p := DefaultPolicy()
	p.Mode, p.BaseURL = ModePrimary, r.srv.URL
	p.FallbackProviders = []string{"gemini"}
	if mutate != nil {
		mutate(&p)
	}
	r.setPolicy(p)
	return r
}

func (r *rig) setPolicy(p Policy) {
	r.t.Helper()
	if err := r.st.Write(context.Background(), func(tx *sql.Tx) error { return SavePolicy(context.Background(), tx, p, r.clk.Now()) }); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) call(ctx context.Context, taskID, class string) (*gateway.Result, error) {
	return r.svc.Router.Complete(ctx, gateway.Budget{TaskID: taskID, TaskLimitUSD: 1}, gateway.Request{Tier: "standard", Purpose: "plan", DataClass: class,
		Messages: []gateway.Message{{Role: "user", Content: "hi"}}}, func(c context.Context, b gateway.Budget, q gateway.Request) (*gateway.Result, error) {
		r.legacy++
		r.last, r.lastB = q, b
		return &gateway.Result{Content: "from legacy", Provider: "gemini", RouteID: "g", APIModelID: "g1"}, nil
	})
}

// healthy makes the monitor believe NEXUS is up.
func (r *rig) healthy() { r.svc.Mon.Observe(true, "OK", "probe") }

// down feeds sustained failures.
func (r *rig) down() {
	for i := 0; i < 3; i++ {
		r.svc.Mon.Observe(false, "MODEL_UNAVAILABLE", "probe")
	}
}

func (r *rig) audit(typ string) int {
	var n int
	r.st.R.QueryRow("select count(*) from audit_events where type=?", typ).Scan(&n)
	return n
}

// AT-01: a low-confidence answer, a missing-knowledge answer, a slow call and a busy NEXUS never reach an external provider.
func TestAT01_HealthyNexusNeverTriggersExternalAI(t *testing.T) {
	r := newRig(t, nil)
	r.healthy()
	cases := map[string]func(http.ResponseWriter, Request){
		"low confidence": func(w http.ResponseWriter, q Request) { writeResult(w, 200, q, "SUCCEEDED", "maybe", "low") },
		"insufficient evidence": func(w http.ResponseWriter, q Request) {
			writeResult(w, 200, q, "INSUFFICIENT_EVIDENCE", "", "low")
		},
		"overloaded (queue depth)":       func(w http.ResponseWriter, q Request) { errEnv(w, 503, "OVERLOADED") },
		"rate limited":                   func(w http.ResponseWriter, q Request) { errEnv(w, 429, "RATE_LIMITED") },
		"deadline exceeded":              func(w http.ResponseWriter, q Request) { errEnv(w, 504, "DEADLINE_EXCEEDED") },
		"missing knowledge, bad request": func(w http.ResponseWriter, q Request) { errEnv(w, 400, "INVALID_REQUEST") },
	}
	for name, fn := range cases {
		r.f.submit = fn
		for i := 0; i < 5; i++ { // repeated, so a "sustained" pattern of non-health errors is also covered
			r.call(context.Background(), "t1", "public")
		}
		if r.legacy != 0 {
			t.Fatalf("%s: an external provider was called %d times", name, r.legacy)
		}
		if st, _ := r.svc.Mon.State(); st != Healthy {
			t.Fatalf("%s: capacity/latency/uncertainty must not make NEXUS look unavailable, state=%s", name, st)
		}
	}
	// A slow call that exceeds the caller's deadline.
	r.f.submit = func(w http.ResponseWriter, q Request) {
		time.Sleep(300 * time.Millisecond)
		writeResult(w, 200, q, "SUCCEEDED", "late", "high")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := r.call(ctx, "t1", "public"); err == nil {
		t.Fatal("a slow call returned")
	}
	if r.legacy != 0 || r.audit("FALLBACK_AUTHORIZED") != 0 {
		t.Fatal("latency triggered a fallback")
	}
	// And the normal path serves from NEXUS.
	r.f.submit = ok("from nexus")
	res, err := r.call(context.Background(), "t1", "public")
	if err != nil || res.Content != "from nexus" || res.Provider != "nexus" || r.legacy != 0 {
		t.Fatalf("%v %+v legacy=%d", err, res, r.legacy)
	}
}

// AT-02: NEXUS is down with valid evidence, yet AURA's policy still decides; if it denies or cannot be evaluated, no external provider is called.
func TestAT02_FallbackNeedsEvidenceAndAURAPolicy(t *testing.T) {
	t.Run("no provider approved by the owner", func(t *testing.T) {
		r := newRig(t, func(p *Policy) { p.FallbackProviders = nil })
		r.down()
		if _, err := r.call(context.Background(), "t1", "public"); !errors.Is(err, ErrFallbackNotAuthorized) || r.legacy != 0 {
			t.Fatalf("%v legacy=%d", err, r.legacy)
		}
	})
	t.Run("data class not permitted", func(t *testing.T) {
		r := newRig(t, nil) // default fallback data class: public only
		r.down()
		if _, err := r.call(context.Background(), "t1", "private"); !errors.Is(err, ErrDataClassNotAllowed) || r.legacy != 0 {
			t.Fatalf("%v legacy=%d", err, r.legacy)
		}
	})
	t.Run("policy cannot be evaluated", func(t *testing.T) {
		r := newRig(t, nil)
		r.down()
		r.call(context.Background(), "warm", "public") // the router has seen a good policy
		r.legacy = 0
		r.st.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec("update settings set value='{broken' where key=?", SettingKey)
			return err
		})
		if _, err := r.call(context.Background(), "t2", "public"); !errors.Is(err, ErrFallbackNotAuthorized) || r.legacy != 0 {
			t.Fatalf("%v legacy=%d", err, r.legacy)
		}
	})
	t.Run("health unknown (no or stale evidence)", func(t *testing.T) {
		r := newRig(t, nil)
		r.down()
		r.clk.Add(2 * time.Minute) // the evidence is stale
		if _, err := r.svc.Auth.Authorize(context.Background(), "t1", "plan", "public"); !errors.Is(err, ErrNexusHealthUnknown) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a degraded NEXUS is not an unavailable one", func(t *testing.T) {
		r := newRig(t, nil)
		r.svc.Mon.Observe(false, "MODEL_UNAVAILABLE", "probe")
		r.svc.Mon.Observe(false, "MODEL_UNAVAILABLE", "probe")
		if _, err := r.svc.Auth.Authorize(context.Background(), "t1", "plan", "public"); !errors.Is(err, ErrFallbackNotAuthorized) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("authorized: scoped, audited, bounded", func(t *testing.T) {
		r := newRig(t, func(p *Policy) { p.FallbackMaxCalls = 2; p.FallbackBudgetUSD = 0.1 })
		r.down()
		for i := 0; i < 2; i++ {
			res, err := r.call(context.Background(), "t1", "public")
			if err != nil || res.Provider != "gemini" {
				t.Fatalf("%v %+v", err, res)
			}
		}
		if len(r.last.OnlyProviders) != 1 || r.last.OnlyProviders[0] != "gemini" || r.lastB.TaskLimitUSD != 0.1 {
			t.Fatalf("scope not applied: providers=%v budget=%v", r.last.OnlyProviders, r.lastB.TaskLimitUSD)
		}
		if _, err := r.call(context.Background(), "t1", "public"); !errors.Is(err, ErrFallbackNotAuthorized) || r.legacy != 2 {
			t.Fatalf("the call limit was not enforced: %v legacy=%d", err, r.legacy)
		}
		var ev, trig string
		r.st.R.QueryRow("select evidence_ref, trigger_code from fallback_authorizations").Scan(&ev, &trig)
		if trig != TriggerNexusUnavailable || !strings.HasPrefix(ev, "nexus-health:") {
			t.Fatalf("trigger %q evidence %q", trig, ev)
		}
		if r.audit("FALLBACK_AUTHORIZED") != 1 || r.audit("FALLBACK_USED") != 2 {
			t.Fatalf("audit authorized=%d used=%d", r.audit("FALLBACK_AUTHORIZED"), r.audit("FALLBACK_USED"))
		}
	})
}

// AT-03: when NEXUS recovers, authorizations are revoked and cognition goes back to NEXUS.
func TestAT03_RecoveryRevokesFallbackAndReturnsToNexus(t *testing.T) {
	r := newRig(t, nil)
	r.down()
	if _, err := r.call(context.Background(), "t1", "public"); err != nil || r.legacy != 1 {
		t.Fatalf("%v legacy=%d", err, r.legacy)
	}
	var active int
	r.st.R.QueryRow("select count(*) from fallback_authorizations where state='active'").Scan(&active)
	if active != 1 {
		t.Fatalf("active %d", active)
	}
	// NEXUS comes back: two good probes, after the hold-down.
	r.clk.Add(40 * time.Second)
	r.f.setReady(200)
	r.svc.ProbeOnce(context.Background())
	r.svc.ProbeOnce(context.Background())
	if st, _ := r.svc.Mon.State(); st != Healthy {
		t.Fatalf("state %s", st)
	}
	r.st.R.QueryRow("select count(*) from fallback_authorizations where state='active'").Scan(&active)
	var reason string
	r.st.R.QueryRow("select revoke_reason from fallback_authorizations").Scan(&reason)
	if active != 0 || reason != "NEXUS_RECOVERED" || r.audit("FALLBACK_REVOKED") != 1 {
		t.Fatalf("active=%d reason=%q audit=%d", active, reason, r.audit("FALLBACK_REVOKED"))
	}
	if err := r.svc.Auth.Use(context.Background(), firstAuthorization(t, r)); err == nil {
		t.Fatal("a revoked authorization was used")
	}
	before := r.legacy
	res, err := r.call(context.Background(), "t1", "public")
	if err != nil || res.Provider != "nexus" || r.legacy != before {
		t.Fatalf("after recovery: %v %+v legacy %d->%d", err, res, before, r.legacy)
	}
}

func firstAuthorization(t *testing.T, r *rig) string {
	var id string
	if err := r.st.R.QueryRow("select id from fallback_authorizations limit 1").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOwnerDisableAllowsScopedFallbackWithoutCallingNexus(t *testing.T) {
	r := newRig(t, nil)
	r.healthy()
	p, _ := LoadPolicy(context.Background(), r.st.R)
	p.OwnerDisabledUntil = r.clk.Now().Add(5 * time.Minute).UnixMilli()
	r.setPolicy(p)
	res, err := r.call(context.Background(), "t1", "public")
	if err != nil || res.Provider != "gemini" || r.f.count() != 0 {
		t.Fatalf("%v %+v nexus calls=%d", err, res, r.f.count())
	}
	var trig string
	var exp int64
	r.st.R.QueryRow("select trigger_code, expires_at from fallback_authorizations").Scan(&trig, &exp)
	if trig != TriggerOwnerDisabled || exp > p.OwnerDisabledUntil {
		t.Fatalf("trigger %s expires %d (owner window ends %d)", trig, exp, p.OwnerDisabledUntil)
	}
	// After the owner's window the authorization is gone and NEXUS serves again.
	r.clk.Add(6 * time.Minute)
	r.healthy()
	if err := r.svc.Auth.Use(context.Background(), firstAuthorization(t, r)); err == nil {
		t.Fatal("an expired authorization was used")
	}
}

func TestMonitorHysteresis(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	m := NewMonitor(MonitorConfig{FailThreshold: 3, FailWindow: time.Minute, RecoverSuccesses: 2, HoldDown: 30 * time.Second, StaleAfter: 45 * time.Second}, clk.Now)
	if s, _ := m.State(); s != Unknown {
		t.Fatal(s)
	}
	m.Observe(false, "MODEL_UNAVAILABLE", "call")
	if s, _ := m.State(); s != Degraded {
		t.Fatalf("one transient failure must not make NEXUS unavailable: %s", s)
	}
	m.Observe(true, "OK", "probe")
	m.Observe(true, "OK", "probe")
	if s, _ := m.State(); s != Healthy {
		t.Fatal(s)
	}
	// failures spread over more than the window are not "sustained"
	for i := 0; i < 5; i++ {
		clk.Add(40 * time.Second)
		m.Observe(false, "TRANSPORT", "probe")
	}
	if s, _ := m.State(); s == Unavailable {
		t.Fatalf("slow trickle counted as sustained failure: %s", s)
	}
	m.Observe(true, "OK", "probe")
	m.Observe(true, "OK", "probe")
	for i := 0; i < 3; i++ {
		m.Observe(false, "MODEL_UNAVAILABLE", "probe")
	}
	if s, _ := m.State(); s != Unavailable {
		t.Fatal(s)
	}
	// recovery needs consecutive successes AND the hold-down (anti-flapping)
	m.Observe(true, "OK", "probe")
	m.Observe(true, "OK", "probe")
	if s, _ := m.State(); s != Unavailable {
		t.Fatalf("flipped back inside the hold-down: %s", s)
	}
	clk.Add(31 * time.Second)
	m.Observe(true, "OK", "probe")
	if s, _ := m.State(); s != Healthy {
		t.Fatal(s)
	}
	clk.Add(time.Minute)
	if s, ev := m.State(); s != Unknown || !strings.HasSuffix(ev, ":stale") {
		t.Fatalf("stale evidence must read as unknown: %s %s", s, ev)
	}
}

func TestClientRejectsAnAnswerThatClaimsAuthority(t *testing.T) {
	r := newRig(t, nil)
	r.healthy()
	r.f.submit = func(w http.ResponseWriter, q Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"request_id": q.RequestID, "status": "SUCCEEDED", "result": map[string]any{"content": "do it"}, "execution_authority": "full", "schema_version": "1.0"})
	}
	if _, err := r.call(context.Background(), "t1", "public"); !errors.Is(err, ErrProtocol) || r.legacy != 0 {
		t.Fatalf("%v legacy=%d", err, r.legacy)
	}
	r.f.submit = func(w http.ResponseWriter, q Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"request_id": "someone-else", "status": "SUCCEEDED", "result": map[string]any{"content": "x"}, "execution_authority": "none", "schema_version": "1.0"})
	}
	if _, err := r.call(context.Background(), "t1", "public"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("%v", err)
	}
}

func TestStructuredOutputFromNexusIsValidatedNotReplaced(t *testing.T) {
	r := newRig(t, nil)
	r.healthy()
	r.f.submit = ok("this is not json")
	_, err := r.svc.Router.Complete(context.Background(), gateway.Budget{TaskID: "t1"}, gateway.Request{Tier: "standard", ResponseSchema: json.RawMessage(`{"type":"object","required":["a"]}`),
		Messages: []gateway.Message{{Role: "user", Content: "x"}}}, func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error) {
		r.legacy++
		return &gateway.Result{}, nil
	})
	if !errors.Is(err, gateway.ErrInvalidOutput) || r.legacy != 0 {
		t.Fatalf("%v legacy=%d", err, r.legacy)
	}
}

func TestShadowModeServesFromLegacyAndMeasuresNexus(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.Mode = ModeShadow })
	res, err := r.call(context.Background(), "t1", "public")
	if err != nil || res.Provider != "gemini" {
		t.Fatalf("%v %+v", err, res)
	}
	deadline := time.Now().Add(3 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		r.st.R.QueryRow("select count(*) from nexus_shadow").Scan(&n)
		if n == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var okv, nchars, lchars int
	r.st.R.QueryRow("select ok, nexus_chars, legacy_chars from nexus_shadow").Scan(&okv, &nchars, &lchars)
	if n != 1 || okv != 1 || nchars != len("from nexus") || lchars != len("from legacy") {
		t.Fatalf("shadow rows=%d ok=%d sizes=%d/%d", n, okv, nchars, lchars)
	}
}

func TestModeOffIsTheLegacyPathUntouched(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.Mode = ModeOff })
	res, err := r.call(context.Background(), "t1", "private")
	if err != nil || res.Provider != "gemini" || r.f.count() != 0 {
		t.Fatalf("%v %+v nexus calls=%d", err, res, r.f.count())
	}
}

func TestPolicyValidation(t *testing.T) {
	p := DefaultPolicy()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Mode = ModePrimary
	if p.Validate() == nil {
		t.Fatal("primary without a base_url")
	}
	p.BaseURL = "http://n"
	p.FallbackDataClass = []string{"secret"}
	if p.Validate() == nil {
		t.Fatal("unknown data class")
	}
}

func TestTeacherGate(t *testing.T) {
	var calls int
	var last gateway.Request
	mk := func(mutate func(*Policy)) (*rig, *Teacher) {
		r := newRig(t, func(p *Policy) {
			p.TeacherEnabled, p.TeacherProviders = true, []string{"openrouter"}
			if mutate != nil {
				mutate(p)
			}
		})
		tg := &Teacher{St: r.st, Now: r.clk.Now, Legacy: func(ctx context.Context, b gateway.Budget, q gateway.Request) (*gateway.Result, error) {
			calls++
			last = q
			return &gateway.Result{Content: "the answer", Provider: "openrouter", APIModelID: "m"}, nil
		}}
		return r, tg
	}
	t.Run("off by default", func(t *testing.T) {
		calls = 0
		r := newRig(t, nil)
		tg := &Teacher{St: r.st, Now: r.clk.Now, Legacy: func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error) {
			calls++
			return &gateway.Result{}, nil
		}}
		_, err := tg.Ask(context.Background(), TeacherInput{Question: "How does TCP slow start work?"})
		var d *TeacherDenied
		if !errors.As(err, &d) || d.Code != "TEACHER_DISABLED" || calls != 0 {
			t.Fatalf("%v calls=%d", err, calls)
		}
	})
	t.Run("answers a public question as untrusted external-ai", func(t *testing.T) {
		calls = 0
		r, tg := mk(nil)
		a, err := tg.Ask(context.Background(), TeacherInput{Gap: "networking", Question: "How does TCP slow start work?", ExpectedForm: "short"})
		if err != nil || a.Trust != "untrusted" || a.SourceType != "external-ai" || a.Answer != "the answer" || calls != 1 {
			t.Fatalf("%v %+v calls=%d", err, a, calls)
		}
		if last.DataClass != "public" || len(last.OnlyProviders) != 1 || last.OnlyProviders[0] != "openrouter" || last.Purpose != "teacher" {
			t.Fatalf("scope: %+v", last)
		}
		var n int
		r.st.R.QueryRow("select count(*) from teacher_requests where state='answered'").Scan(&n)
		if n != 1 || r.audit("TEACHER_ASKED") != 1 {
			t.Fatalf("rows=%d audit=%d", n, r.audit("TEACHER_ASKED"))
		}
	})
	t.Run("refuses what looks personal or secret", func(t *testing.T) {
		calls = 0
		r, tg := mk(nil)
		for _, q := range []string{"Please check budi.santoso@gmail.com for me", "Call 0812-3456-7890 about the order", "My account number is 1234567890123", "password: hunter2 is it safe?",
			"fetch https://user:pw@host.example/x", "token sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEF", "", strings.Repeat("a", 1600)} {
			if _, err := tg.Ask(context.Background(), TeacherInput{Question: q}); err == nil {
				t.Errorf("accepted %q", q)
			}
		}
		if calls != 0 || r.audit("TEACHER_DENIED") != 8 {
			t.Fatalf("a refused question reached a provider or was not audited: calls=%d audit=%d", calls, r.audit("TEACHER_DENIED"))
		}
	})
	t.Run("daily limit", func(t *testing.T) {
		calls = 0
		_, tg := mk(func(p *Policy) { p.TeacherMaxPerDay = 2 })
		for i := 0; i < 2; i++ {
			if _, err := tg.Ask(context.Background(), TeacherInput{Question: "What is a B-tree?"}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := tg.Ask(context.Background(), TeacherInput{Question: "What is a B+ tree?"})
		var d *TeacherDenied
		if !errors.As(err, &d) || d.Code != "TEACHER_LIMIT" || calls != 2 {
			t.Fatalf("%v calls=%d", err, calls)
		}
	})
}

func TestSanitizeAcceptsOrdinaryQuestions(t *testing.T) {
	for _, q := range []string{"What is the difference between a mutex and a semaphore?", "Explain Indonesian VAT (PPN) at 11% for a small online shop.", "Apa itu REST API dan bagaimana idempotensi bekerja?"} {
		if d := Sanitize(q); d != nil {
			t.Errorf("%q refused: %v", q, d)
		}
	}
}
