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
	lessons  func(w http.ResponseWriter, in map[string]any)
	requests int
	prios    []string
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
		f.prios = append(f.prios, req.Priority)
		fn := f.submit
		f.mu.Unlock()
		fn(w, req)
	})
	mux.HandleFunc("POST /api/v1/knowledge/lessons", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		fn := f.lessons
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fn(w, in)
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
	t          *testing.T
	st         *store.Store
	f          *fake
	srv        *httptest.Server
	clk        *clock
	svc        *Service
	legacy     int  // calls that were SERVED by the legacy path
	freeOK     bool // a free route exists and answers
	freeServed int
	last       gateway.Request
	lastB      gateway.Budget
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
	r.svc = NewService(st, r.legacyFn, func() string { return "tok" }, "aura-core", "w1", MonitorConfig{FailThreshold: 3, FailWindow: time.Minute, RecoverSuccesses: 2, HoldDown: 30 * time.Second, StaleAfter: 45 * time.Second})
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

// legacyFn stands in for the gateway's provider path. A "free" request is served only when a free route exists (r.freeOK); otherwise it fails like a tier with no free route.
func (r *rig) legacyFn(ctx context.Context, b gateway.Budget, q gateway.Request) (*gateway.Result, error) {
	if q.RouteClass == "free" {
		if !r.freeOK {
			return nil, gateway.ErrNoRoute
		}
		r.freeServed++
		r.last, r.lastB = q, b
		return &gateway.Result{Content: "from legacy", Provider: "openrouter", RouteID: "free", APIModelID: "f1", Free: true}, nil
	}
	r.legacy++
	r.last, r.lastB = q, b
	return &gateway.Result{Content: "from legacy", Provider: "gemini", RouteID: "g", APIModelID: "g1"}, nil
}

func (r *rig) setPolicy(p Policy) {
	r.t.Helper()
	if err := r.st.Write(context.Background(), func(tx *sql.Tx) error { return SavePolicy(context.Background(), tx, p, r.clk.Now()) }); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) call(ctx context.Context, taskID, class string) (*gateway.Result, error) {
	return r.svc.Router.Complete(ctx, gateway.Budget{TaskID: taskID, TaskLimitUSD: 1}, gateway.Request{Tier: "standard", Purpose: "plan", DataClass: class,
		Messages: []gateway.Message{{Role: "user", Content: "hi"}}}, r.legacyFn)
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
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if len(r.f.prios) != 1 || r.f.prios[0] != "background" {
		t.Fatalf("a shadow call is evaluation only and must be background work for NEXUS: %v", r.f.prios)
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
			if q.RouteClass == "free" && !r.freeOK {
				return nil, gateway.ErrNoRoute
			}
			calls++
			last = q
			return &gateway.Result{Content: "the answer", Provider: "openrouter", APIModelID: "m", Free: q.RouteClass == "free"}, nil
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

func TestShadowSamplingAndConcurrencyAreBounded(t *testing.T) {
	// 0 %: nothing is sent to NEXUS
	r := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent = ModeShadow, 0 })
	for i := 0; i < 5; i++ {
		r.call(context.Background(), "t1", "public")
	}
	time.Sleep(200 * time.Millisecond)
	if r.f.count() != 0 {
		t.Fatalf("0%% shadow still sent %d calls to NEXUS", r.f.count())
	}
	// 100 % but NEXUS is slow: at most two shadow calls are in flight, the others are skipped (and legacy is never delayed)
	r2 := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent = ModeShadow, 100 })
	release := make(chan struct{})
	r2.f.submit = func(w http.ResponseWriter, q Request) { <-release; writeResult(w, 200, q, "SUCCEEDED", "late", "low") }
	start := time.Now()
	for i := 0; i < 6; i++ {
		if _, err := r2.call(context.Background(), "t1", "public"); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("a slow NEXUS delayed the legacy path")
	}
	time.Sleep(300 * time.Millisecond)
	if n := r2.f.count(); n != 2 {
		t.Fatalf("expected exactly 2 shadow calls in flight, NEXUS saw %d", n)
	}
	close(release)
	// sampling by percentage uses the injected random source
	r3 := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent = ModeShadow, 30 })
	hits := 0
	r3.svc.Router.Rand = func() int { hits++; return map[int]int{1: 10, 2: 90, 3: 29, 4: 30}[hits] }
	for i := 0; i < 4; i++ {
		r3.call(context.Background(), "t1", "public")
		time.Sleep(60 * time.Millisecond)
	}
	if n := r3.f.count(); n != 2 {
		t.Fatalf("30%% sampling with draws 10,90,29,30 should send 2 calls, sent %d", n)
	}
	if err := (Policy{Mode: ModeOff, ShadowPercent: 101, FallbackTTLMinutes: 10}).Validate(); err == nil {
		t.Fatal("shadow_percent above 100 accepted")
	}
}

func TestTeachingCapturesOnlyPublicSanitizedExternalAnswersAndPushesThem(t *testing.T) {
	var got []map[string]any
	r := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent, p.TeachEnabled, p.TeachDailyMax = ModeShadow, 0, true, 2 })
	// a fake lessons endpoint on the same fake NEXUS: accept everything except one source
	r.f.lessons = func(w http.ResponseWriter, in map[string]any) {
		ls, _ := in["lessons"].([]any)
		res := []map[string]any{}
		for i, l := range ls {
			m := l.(map[string]any)
			got = append(got, m)
			res = append(res, map[string]any{"source_id": m["source_id"], "accepted": i != 0, "reason": map[bool]string{true: "", false: "format check failed"}[i != 0]})
		}
		json.NewEncoder(w).Encode(map[string]any{"results": res})
	}
	call := func(class, user, answer string) {
		r.svc.Router.Complete(context.Background(), gateway.Budget{TaskID: "t"}, gateway.Request{Tier: "standard", Purpose: "write", DataClass: class, Messages: []gateway.Message{{Role: "user", Content: user}}},
			func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error) {
				return &gateway.Result{Content: answer, Provider: "gemini", RouteID: "g"}, nil
			})
	}
	count := func() int { var n int; r.st.R.QueryRow("select count(*) from teaching_examples").Scan(&n); return n }
	call("private", "my private question about my salary", "an answer")
	call("public", "contact me at budi@example.com about the plan", "ok")
	call("public", "token sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEF is it safe", "no")
	call("public", "x", "")
	if count() != 0 {
		var q string
		r.st.R.QueryRow("select messages_json from teaching_examples limit 1").Scan(&q)
		t.Fatalf("private, personal, secret or empty examples were kept: %d (%s) counters %v", count(), q, r.svc.Router.TeachingCounts())
	}
	call("public", "Explain what an idempotency key is.", "An idempotency key lets a client retry safely.")
	call("public", "Explain what an idempotency key is.", "An idempotency key lets a client retry safely.") // the same example is kept once
	call("public", "What is a B-tree?", "A balanced search tree used by databases.")
	call("public", "What is a hash map?", "A key-value structure with O(1) average lookup.") // beyond the daily cap of 2
	if count() != 2 {
		t.Fatalf("expected 2 examples (dedup + daily cap), have %d", count())
	}
	sent, acc, err := r.svc.PushLessons(context.Background())
	if err != nil || sent != 2 || acc != 1 {
		t.Fatalf("push: sent=%d accepted=%d err=%v", sent, acc, err)
	}
	if len(got) != 2 || got[0]["data_class"] != "public" || got[0]["source"].(map[string]any)["type"] != "external-ai" {
		t.Fatalf("payload: %v", got)
	}
	var sentN, rej int
	r.st.R.QueryRow("select count(*) from teaching_examples where status='sent'").Scan(&sentN)
	r.st.R.QueryRow("select count(*) from teaching_examples where status='rejected' and reject_reason='format check failed'").Scan(&rej)
	if sentN != 1 || rej != 1 {
		t.Fatalf("statuses sent=%d rejected=%d", sentN, rej)
	}
	// nothing left to send; teaching off sends nothing
	if s2, _, _ := r.svc.PushLessons(context.Background()); s2 != 0 {
		t.Fatalf("pushed again: %d", s2)
	}
}

func TestTeachingIsOffByDefault(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent = ModeShadow, 0 })
	r.svc.Router.Complete(context.Background(), gateway.Budget{}, gateway.Request{Tier: "standard", DataClass: "public", Messages: []gateway.Message{{Role: "user", Content: "hello there"}}},
		func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error) {
			return &gateway.Result{Content: "hi", Provider: "gemini"}, nil
		})
	var n int
	r.st.R.QueryRow("select count(*) from teaching_examples").Scan(&n)
	if n != 0 {
		t.Fatal("an example was kept although the owner did not enable teaching")
	}
}

func TestTeachingExplainsItsSkipsAndAcceptsLongNumbersInPublicPrompts(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent, p.TeachEnabled, p.TeachDailyMax = ModeShadow, 0, true, 50 })
	call := func(class, user, answer, provider string) {
		r.svc.Router.Complete(context.Background(), gateway.Budget{TaskID: "t"}, gateway.Request{Tier: "standard", Purpose: "plan", DataClass: class, Messages: []gateway.Message{{Role: "user", Content: user}}},
			func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error) {
				return &gateway.Result{Content: answer, Provider: provider, RouteID: "g"}, nil
			})
	}
	call("private", "q", "a", "gemini")
	call("public", "when was task 01M7JM9NMXY7PKV5BPNHDBVP1A created? epoch 1794847893764 and 20261117001122", "at that time", "gemini") // ids and timestamps are fine
	call("public", "mail budi@example.com", "ok", "gemini")
	call("public", "x", "from nexus", "nexus")
	call("public", "y", "", "gemini")
	call("public", strings.Repeat("a", 13000), "a", "gemini")
	got := r.svc.Router.TeachingCounts()
	want := map[string]int{"not_public": 1, "kept": 1, "looks_personal_or_secret": 1, "answered_by_nexus": 1, "empty_answer": 1, "too_large": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("counter %s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	var n int
	r.st.R.QueryRow("select count(*) from teaching_examples").Scan(&n)
	if n != 1 {
		t.Fatalf("examples kept: %d", n)
	}
}

func TestCredentialShapesAreRecognisedWithoutAnyLabel(t *testing.T) {
	for _, secret := range []string{"sk-abcdefghijklmnopqrstuvwxyz", "AIzaSyA1234567890abcdefghijklmnop", "gsk_abcdefghijklmnopqrstuvwx", "ghp_abcdefghijklmnopqrstuvwxyz1234", "AKIAABCDEFGHIJKLMNOP",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcDEF123", "-----BEGIN RSA PRIVATE KEY-----"} {
		if Sanitize("please check "+secret+" for me") == nil || SanitizeForTeaching("please check "+secret+" for me") == nil {
			t.Errorf("%q was not recognised as a secret", secret)
		}
	}
	for _, fine := range []string{"The task id is 01M7JM9NMXY7PKV5BPNHDBVP1A and the hash is 3b7e72f1", "Skip this skeleton: sk-1 is not a key", "epoch 1794847893764"} {
		if SanitizeForTeaching(fine) != nil {
			t.Errorf("%q was refused", fine)
		}
	}
}

// Owner decision 2026-11-17: the limits (budget, call counts, daily caps) apply to PAID routes only; free routes are unlimited.
func TestFallbackFreeRoutesAreUnlimitedAndPaidOnesAreLimited(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.FallbackMaxCalls = 2; p.FallbackBudgetUSD = 0.1 })
	r.down()
	r.freeOK = true
	for i := 0; i < 8; i++ { // far beyond max_calls: free routes are not counted
		res, err := r.call(context.Background(), "t1", "public")
		if err != nil || !res.Free {
			t.Fatalf("free call %d: %v %+v", i, err, res)
		}
	}
	var used int
	r.st.R.QueryRow("select used_calls from fallback_authorizations").Scan(&used)
	if used != 0 || r.legacy != 0 || r.freeServed != 8 || r.lastB.TaskLimitUSD != 1 {
		t.Fatalf("free use was counted or capped: used=%d paid served=%d free served=%d budget=%v", used, r.legacy, r.freeServed, r.lastB.TaskLimitUSD)
	}
	r.freeOK = false
	for i := 0; i < 2; i++ {
		if res, err := r.call(context.Background(), "t1", "public"); err != nil || res.Free {
			t.Fatalf("paid call %d: %v %+v", i, err, res)
		}
	}
	if r.lastB.TaskLimitUSD != 0.1 {
		t.Fatalf("the paid budget cap was not applied: %v", r.lastB.TaskLimitUSD)
	}
	if _, err := r.call(context.Background(), "t1", "public"); !errors.Is(err, ErrFallbackNotAuthorized) {
		t.Fatalf("the paid call limit was not enforced: %v", err)
	}
	r.freeOK = true
	if _, err := r.call(context.Background(), "t1", "public"); err != nil {
		t.Fatalf("a free route was refused after the paid limit was used up: %v", err)
	}
	if _, err := r.call(context.Background(), "t2", "private"); !errors.Is(err, ErrDataClassNotAllowed) {
		t.Fatalf("the safety gate must stay: %v", err)
	}
}

func TestTeacherFreeRoutesAreUnlimitedPaidOnesHaveADailyLimit(t *testing.T) {
	var paidCalls, freeCalls int
	r := newRig(t, func(p *Policy) {
		p.TeacherEnabled, p.TeacherProviders, p.TeacherMaxPerDay = true, []string{"openrouter"}, 2
	})
	tg := &Teacher{St: r.st, Now: r.clk.Now, Legacy: func(ctx context.Context, b gateway.Budget, q gateway.Request) (*gateway.Result, error) {
		if q.RouteClass == "free" {
			if !r.freeOK {
				return nil, gateway.ErrNoRoute
			}
			freeCalls++
			return &gateway.Result{Content: "free answer", Provider: "openrouter", Free: true}, nil
		}
		paidCalls++
		if b.TaskLimitUSD != 0.05 {
			t.Errorf("paid budget %v", b.TaskLimitUSD)
		}
		return &gateway.Result{Content: "paid answer", Provider: "gemini"}, nil
	}}
	r.freeOK = true
	for i := 0; i < 10; i++ {
		if a, err := tg.Ask(context.Background(), TeacherInput{Question: "What is a B-tree number " + string(rune('a'+i)) + "?"}); err != nil || a.Answer != "free answer" {
			t.Fatalf("free question %d: %v %+v", i, err, a)
		}
	}
	if freeCalls != 10 || paidCalls != 0 {
		t.Fatalf("free=%d paid=%d", freeCalls, paidCalls)
	}
	r.freeOK = false
	for i := 0; i < 2; i++ {
		if a, err := tg.Ask(context.Background(), TeacherInput{Question: "What is a heap " + string(rune('a'+i)) + "?"}); err != nil || a.Answer != "paid answer" {
			t.Fatalf("paid question %d: %v %+v", i, err, a)
		}
	}
	_, err := tg.Ask(context.Background(), TeacherInput{Question: "What is a trie?"})
	var d *TeacherDenied
	if !errors.As(err, &d) || d.Code != "TEACHER_LIMIT" || paidCalls != 2 {
		t.Fatalf("the paid daily limit: %v paid=%d", err, paidCalls)
	}
	var paidRows int
	r.st.R.QueryRow("select count(*) from teacher_requests where state='answered' and paid=1").Scan(&paidRows)
	if paidRows != 2 {
		t.Fatalf("paid rows %d", paidRows)
	}
}

func TestTeachingCapAppliesToPaidAnswersOnly(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent, p.TeachEnabled, p.TeachDailyMax = ModeShadow, 0, true, 1 })
	ask := func(user string, free bool) {
		r.svc.Router.Complete(context.Background(), gateway.Budget{TaskID: "t"}, gateway.Request{Tier: "standard", Purpose: "write", DataClass: "public", Messages: []gateway.Message{{Role: "user", Content: user}}},
			func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error) {
				return &gateway.Result{Content: "answer to " + user, Provider: "x", Free: free}, nil
			})
	}
	for i := 0; i < 6; i++ {
		ask("free question "+string(rune('a'+i)), true)
	}
	ask("paid question one", false)
	ask("paid question two", false)
	var free, paid int
	r.st.R.QueryRow("select count(*) from teaching_examples where paid=0").Scan(&free)
	r.st.R.QueryRow("select count(*) from teaching_examples where paid=1").Scan(&paid)
	if free != 6 || paid != 1 {
		t.Fatalf("free examples %d (want 6, uncapped), paid %d (want 1, capped)", free, paid)
	}
}

func TestLongExamplesAreKeptLocallyAndNotSentToNexus(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.Mode, p.ShadowPercent, p.TeachEnabled, p.TeachDailyMax = ModeShadow, 0, true, 50 })
	now := time.Now().UnixMilli()
	r.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`insert into teaching_examples(id,purpose,tier,messages_json,answer,provider,route,content_sha256,tokens_est,status,created_at,paid) values('long','plan','standard',?, 'a','gemini','g','h1',1,'new',?,0)`,
			`[{"role":"user","content":"`+strings.Repeat("x", 9500)+`"}]`, now)
		return err
	})
	r.svc.PushLessons(context.Background())
	var st, why string
	r.st.R.QueryRow("select status, coalesce(reject_reason,'') from teaching_examples where id='long'").Scan(&st, &why)
	if st != "rejected" || !strings.Contains(why, "fine-tuning") {
		t.Fatalf("status=%s why=%s", st, why)
	}
}

// ADR-0079 (owner decision 2026-11-17, option A): a heavy PUBLIC task that NEXUS cannot take goes to an external AI (free routes first); NEXUS learns from the answer.
func overflowRefusal(w http.ResponseWriter, _ Request) {
	w.WriteHeader(400)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "input " + ContextOverflowMarker + " (about 4662 + 2048 tokens > 4096)", "retryable": false, "request_id": "r", "trace_id": "t", "schema_version": "1.0"}})
}

func lastTrigger(t *testing.T, r *rig) string {
	var d string
	r.st.R.QueryRow("select coalesce(group_concat(trigger_code),'') from fallback_authorizations").Scan(&d)
	return d
}

func TestOverflowIsOffUnlessTheOwnerTurnsItOn(t *testing.T) {
	r := newRig(t, nil)
	r.f.submit = overflowRefusal
	_, err := r.call(context.Background(), "t1", "public")
	var ne *Error
	if !errors.As(err, &ne) || ne.Code != "INVALID_REQUEST" || r.legacy+r.freeServed != 0 {
		t.Fatalf("a refused heavy task reached an external provider without the owner's switch: %v legacy=%d free=%d", err, r.legacy, r.freeServed)
	}
}

func TestOverflowServesAHeavyPublicTaskExternallyFreeFirstAndLeavesTheHealthMonitorAlone(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.OverflowToExternal = true })
	r.f.submit = overflowRefusal
	r.freeOK = true
	for i := 0; i < 3; i++ {
		res, err := r.call(context.Background(), "t1", "public")
		if err != nil || !res.Free || res.Content != "from legacy" {
			t.Fatalf("call %d: %v %+v", i, err, res)
		}
	}
	if lastTrigger(t, r) != TriggerCapacity {
		t.Fatalf("trigger: %q", lastTrigger(t, r))
	}
	if st, _ := r.svc.Mon.State(); st == Unavailable {
		t.Fatal("a capacity refusal was counted as an outage")
	}
	var n int
	r.st.R.QueryRow("select count(*) from audit_events where type='FALLBACK_USED' and data_json like '%NEXUS_CAPACITY%' or (type='FALLBACK_USED' and data_json like '%free%')").Scan(&n)
	if n == 0 {
		t.Fatal("the overflow use was not audited")
	}
}

func TestOverflowNeverTakesPrivateDataAndNeverTakesAnOrdinaryBadRequest(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.OverflowToExternal = true })
	r.f.submit = overflowRefusal
	// a private call never takes the overflow path at all (2026-11-18): it gets NEXUS's own answer, and nothing goes outside
	if _, err := r.call(context.Background(), "t1", "private"); err == nil || r.legacy+r.freeServed != 0 || !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("private data left through the overflow path: %v", err)
	}
	r.f.submit = func(w http.ResponseWriter, _ Request) { errEnv(w, 400, "INVALID_REQUEST") } // a bad request that is not about capacity
	if _, err := r.call(context.Background(), "t2", "public"); err == nil || r.legacy+r.freeServed != 0 {
		t.Fatalf("an ordinary bad request was served externally: %v", err)
	}
}

func TestOverflowAlsoCoversACallNexusCannotFinishInTime(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.OverflowToExternal, p.OverflowTimeoutSec = true, 1 })
	r.freeOK = true
	r.f.submit = func(w http.ResponseWriter, q Request) {
		time.Sleep(1800 * time.Millisecond)
		writeResult(w, 200, q, "SUCCEEDED", "too late", "low")
	}
	res, err := r.call(context.Background(), "t1", "public")
	if err != nil || res.Content != "from legacy" || lastTrigger(t, r) != TriggerCapacity {
		t.Fatalf("%v %+v %q", err, res, lastTrigger(t, r))
	}
}

func TestOverflowDoesNotTouchACallNexusCanTake(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.OverflowToExternal = true })
	res, err := r.call(context.Background(), "t1", "public")
	if err != nil || res.Content != "from nexus" || r.legacy+r.freeServed != 0 {
		t.Fatalf("%v %+v", err, res)
	}
}

// Long-poll (2026-11-17): the client asks NEXUS to hold the status read (wait_s) so the answer is not up to one poll interval late; a NEXUS that
// ignores wait_s answers at once and the client keeps its old poll pause instead of spinning.
func TestSubmitLongPollsAndStillPacesAnOldNexus(t *testing.T) {
	for _, holds := range []bool{true, false} {
		var mu sync.Mutex
		reads, waits := 0, []string{}
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/v1/cognitive/requests", func(w http.ResponseWriter, r *http.Request) {
			var q Request
			json.NewDecoder(r.Body).Decode(&q)
			writeResult(w, 202, q, "QUEUED", "", "low")
		})
		mux.HandleFunc("GET /api/v1/cognitive/requests/{id}", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			reads++
			n := reads
			waits = append(waits, r.URL.Query().Get("wait_s"))
			mu.Unlock()
			status := "RUNNING"
			if holds || n >= 3 {
				status = "SUCCEEDED" // a holding NEXUS answers the first read once the work is done
			}
			writeResult(w, 200, Request{RequestID: r.PathValue("id")}, status, "jawaban", "high")
		})
		srv := httptest.NewServer(mux)
		c := &Client{BaseURL: func() string { return srv.URL }, Token: func() string { return "tok" }, Poll: 80 * time.Millisecond}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t0 := time.Now()
		res, err := c.Submit(ctx, Request{RequestID: "r-lp", SchemaVersion: "1.0"})
		took := time.Since(t0)
		cancel()
		srv.Close()
		if err != nil || res.Status != "SUCCEEDED" {
			t.Fatalf("holds=%v: %v %+v", holds, err, res)
		}
		if waits[0] != "10" {
			t.Fatalf("holds=%v: the read must ask NEXUS to wait, got wait_s=%q", holds, waits[0])
		}
		if holds && (reads != 1 || took > 60*time.Millisecond) {
			t.Fatalf("a holding NEXUS needs one read and no client pause: reads=%d took=%v", reads, took)
		}
		if !holds && (reads != 3 || took < 150*time.Millisecond) {
			t.Fatalf("an old NEXUS must still be polled at the poll pace: reads=%d took=%v", reads, took)
		}
	}
}

// Owner 2026-11-18: in primary mode a private call has nowhere to overflow to, so NEXUS gets the long private timeout, not the 45 s overflow one.
func TestAPrivateCallIsNotCutAtTheOverflowTimeout(t *testing.T) {
	r := newRig(t, func(p *Policy) { p.OverflowToExternal, p.OverflowTimeoutSec, p.PrivateTimeoutSec = true, 1, 30 })
	r.f.submit = func(w http.ResponseWriter, q Request) {
		time.Sleep(1800 * time.Millisecond) // longer than the overflow timeout
		writeResult(w, 200, q, "SUCCEEDED", "from nexus, slowly", "high")
	}
	res, err := r.call(context.Background(), "t1", "private")
	if err != nil || res.Content != "from nexus, slowly" || r.legacy+r.freeServed != 0 {
		t.Fatalf("%v %+v", err, res)
	}
}
