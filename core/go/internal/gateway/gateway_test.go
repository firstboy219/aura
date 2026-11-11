package gateway_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/obs"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

var routes = map[string][]config.ModelRoute{
	"standard": {
		{DisplayName: "A", APIModelID: "a", RouteID: "route/a", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000},
		{DisplayName: "B", APIModelID: "b", RouteID: "route/b", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000},
	},
}

const schema = `{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"string"}}}`

func setup(t *testing.T, p gateway.Provider) (*gateway.Gateway, *store.Store, string) {
	t.Helper()
	d := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tk := &task.Task{Objective: "x", ConfigHash: "h"}
	s.Write(context.Background(), func(tx *sql.Tx) error { return task.Insert(context.Background(), tx, tk) })
	g := gateway.New(p, routes, budget.New(s, 10))
	g.SetBackoff(func(int) time.Duration { return time.Millisecond })
	return g, s, tk.ID
}

func req() gateway.Request {
	return gateway.Request{Tier: "standard", Messages: []gateway.Message{{Role: "user", Content: "hi"}}, ResponseSchema: json.RawMessage(schema)}
}

func usageRows(s *store.Store) int {
	var n int
	s.R.QueryRow("select count(*) from usage_records where cost_actual_usd is not null").Scan(&n)
	return n
}

func TestRetryIsBoundedThenFallback(t *testing.T) {
	transient := &gateway.ProviderError{Status: 503, Transient: true, Msg: "busy"}
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Err: transient}, {Err: transient}, {Err: transient}, {Content: `{"answer":"ok"}`}}}
	g, s, id := setup(t, f)
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req())
	if err != nil {
		t.Fatal(err)
	}
	if res.RouteID != "route/b" || string(res.Structured) != `{"answer":"ok"}` {
		t.Fatalf("%+v", res)
	}
	if strings.Join(f.Routes, ",") != "route/a,route/a,route/a,route/b" {
		t.Fatalf("routes %v", f.Routes)
	}
	if usageRows(s) != 4 {
		t.Fatalf("every attempt must be reconciled, got %d", usageRows(s))
	}
}

func TestInvalidOutputRepairOnceThenFail(t *testing.T) {
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Content: `{"wrong":1}`}, {Content: `{"answer":"fixed"}`}}}
	g, _, id := setup(t, f)
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req())
	if err != nil || string(res.Structured) != `{"answer":"fixed"}` {
		t.Fatalf("%v %+v", err, res)
	}
	last := f.Calls[1].Messages
	if !strings.Contains(last[len(last)-1].Content, "did not match") {
		t.Fatal("repair prompt missing validation error")
	}

	f2 := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Content: `nope`}, {Content: `{"answer":5}`}, {Content: `{"answer":"x"}`}}}
	g2, _, id2 := setup(t, f2)
	_, err = g2.Complete(context.Background(), gateway.Budget{TaskID: id2, TaskLimitUSD: 1}, req())
	if !errors.Is(err, gateway.ErrInvalidOutput) || f2.CallCount() != 2 {
		t.Fatalf("want invalid output after exactly 2 calls, got %v after %d", err, f2.CallCount())
	}
}

func TestNonTransientErrorFallsBackWithoutRetry(t *testing.T) {
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Err: &gateway.ProviderError{Status: 404, Msg: "no model"}}, {Content: `{"answer":"b"}`}}}
	g, _, id := setup(t, f)
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req()); err != nil {
		t.Fatal(err)
	}
	if f.CallCount() != 2 {
		t.Fatalf("calls %d", f.CallCount())
	}
}

func TestBudgetStopsBeforeCall(t *testing.T) {
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Content: `{"answer":"x"}`}}}
	g, _, id := setup(t, f)
	_, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 0.000001}, req())
	if !errors.Is(err, budget.ErrTaskBudget) || f.CallCount() != 0 {
		t.Fatalf("%v calls=%d", err, f.CallCount())
	}
}

func TestCancelStopsInFlightCall(t *testing.T) {
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Content: `{"answer":"x"}`, Delay: 10 * time.Second}}}
	g, s, id := setup(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := g.Complete(ctx, gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req())
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	var outcome string
	s.R.QueryRow("select outcome from usage_records").Scan(&outcome)
	if outcome != "cancelled" {
		t.Fatalf("outcome %q", outcome)
	}
}

// The OpenRouter adapter sends the key only in the Authorization header and
// never logs it; it requests native structured output and usage accounting.
func TestOpenRouterAdapter(t *testing.T) {
	const key = "sk-or-v1-test-key-0123456789"
	obs.RegisterSecret(key)
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"id":"gen-1","choices":[{"message":{"content":"{\"answer\":\"hi\"}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.0003}}`))
	}))
	defer srv.Close()
	or := gateway.NewOpenRouter(srv.URL, key, 5*time.Second)
	at, err := or.Complete(context.Background(), routes["standard"][0], req())
	if err != nil || at.Content != `{"answer":"hi"}` || *at.CostUSD != 0.0003 {
		t.Fatalf("%v %+v", err, at)
	}
	rf := got["response_format"].(map[string]any)
	if rf["type"] != "json_schema" || got["model"] != "route/a" {
		t.Fatalf("%v", got)
	}
	// OL-01 retry: reasoning is switched off explicitly, not merely omitted.
	off := routes["standard"][0]
	off.ReasoningEffort = gateway.ReasoningOff
	if _, err := or.Complete(context.Background(), off, req()); err != nil {
		t.Fatal(err)
	}
	if r, _ := got["reasoning"].(map[string]any); r["effort"] != "none" {
		t.Fatalf("reasoning not disabled explicitly: %v", got["reasoning"])
	}
	var buf bytes.Buffer
	slog.New(obs.Handler{Handler: slog.NewJSONHandler(&buf, nil)}).Info("req", "auth", "Bearer "+key)
	if strings.Contains(buf.String(), key) {
		t.Fatal("key leaked to log")
	}
}

func TestOpenRouterErrorsClassified(t *testing.T) {
	status := 429
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"error":{"code":429,"message":"rate limited"}}`))
	}))
	defer srv.Close()
	or := gateway.NewOpenRouter(srv.URL, "k", 5*time.Second)
	_, err := or.Complete(context.Background(), routes["standard"][0], req())
	var pe *gateway.ProviderError
	if !errors.As(err, &pe) || !pe.Transient {
		t.Fatalf("429 should be transient: %v", err)
	}
	status = 400
	_, err = or.Complete(context.Background(), routes["standard"][0], req())
	if !errors.As(err, &pe) || pe.Transient {
		t.Fatalf("400 should be permanent: %v", err)
	}
}

func TestVerifyRoutes(t *testing.T) {
	cat := map[string]gateway.ModelInfo{
		"route/a": {RouteID: "route/a", StructuredOutput: true, PriceInPerMTok: 1, PriceOutPerMTok: 2},
		"route/b": {RouteID: "route/b", StructuredOutput: true, PriceInPerMTok: 5, PriceOutPerMTok: 2},
	}
	checks := gateway.VerifyRoutes(routes, cat)
	if checks[0].Health != "ok" || checks[1].Health != "price_changed" {
		t.Fatalf("%+v", checks)
	}
	if gateway.VerifyRoutes(routes, nil)[0].Health != "unknown" {
		t.Fatal("nil catalog must be unknown")
	}
	_, s, _ := setup(t, &gatewaytest.Fake{})
	if err := gateway.SyncRegistry(context.Background(), s, "https://x", "/run/k", routes, checks); err != nil {
		t.Fatal(err)
	}
	var n int
	s.R.QueryRow("select count(*) from model_tiers where tier='standard'").Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
}

// A definite HTTP error (e.g. 402 insufficient credits) is not billed; the
// provider message reaches the caller.
func TestHTTPErrorIsNotCharged(t *testing.T) {
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Err: &gateway.ProviderError{Status: 402, Msg: "Insufficient credits"}}, {Err: &gateway.ProviderError{Status: 402, Msg: "Insufficient credits"}}}}
	g, s, id := setup(t, f)
	_, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req())
	if err == nil || !strings.Contains(err.Error(), "Insufficient credits") {
		t.Fatalf("message lost: %v", err)
	}
	var spent float64
	s.R.QueryRow("select coalesce(sum(cost_actual_usd),0) from usage_records").Scan(&spent)
	if spent != 0 {
		t.Fatalf("charged %v for rejected calls", spent)
	}
}

func freeSetup(t *testing.T, f *gatewaytest.Fake) (*gateway.Gateway, *store.Store, string) {
	g, s, id := setup(t, f)
	free := map[string][]config.ModelRoute{"standard": {
		{DisplayName: "Free", APIModelID: "free", RouteID: "free/model:free", Free: true, PromptLogging: "may_log", StructuredOutput: "prompt", MaxOutputTokens: 500},
		{DisplayName: "Paid", APIModelID: "paid", RouteID: "paid/model", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 500},
	}}
	g = gateway.New(f, free, budget.New(s, 10))
	g.SetBackoff(func(int) time.Duration { return time.Millisecond })
	return g, s, id
}

// Private task content never reaches a route that may log prompts.
func TestPrivateTasksSkipPromptLoggingRoutes(t *testing.T) {
	f := &gatewaytest.Fake{Respond: func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
		return gatewaytest.Reply{Content: `{"answer":"x"}`}
	}}
	g, _, id := freeSetup(t, f)
	r := req()
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, r); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.Routes, ",") != "paid/model" {
		t.Fatalf("private task used %v", f.Routes)
	}
	r.DataClass = "public"
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, r)
	if err != nil || res.RouteID != "free/model:free" {
		t.Fatalf("public task: %v %+v", err, res)
	}
	// Prompt mode: the schema travels in a system message (no native response_format needed).
	last := f.Calls[len(f.Calls)-1].Messages
	if !strings.Contains(last[len(last)-1].Content, `"required":["answer"]`) {
		t.Fatal("schema not in prompt")
	}
	onlyFree := map[string][]config.ModelRoute{"standard": {{DisplayName: "F", APIModelID: "f", RouteID: "f:free", Free: true, PromptLogging: "may_log"}}}
	g2 := gateway.New(f, onlyFree, nil)
	if _, err := g2.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req()); !errors.Is(err, gateway.ErrNoRoute) {
		t.Fatalf("private task with only may_log routes: %v", err)
	}
}

func TestFreeRouteReservesZeroAndCounts(t *testing.T) {
	zero := 0.0
	f := &gatewaytest.Fake{Respond: func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
		return gatewaytest.Reply{Content: `{"answer":"x"}`, CostUSD: &zero}
	}}
	g, s, id := freeSetup(t, f)
	r := req()
	r.DataClass = "public"
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 0.0001, MaxCalls: 1}, r); err != nil {
		t.Fatal(err)
	}
	var n int
	var spent float64
	s.R.QueryRow("select count(*), sum(cost_actual_usd) from usage_records").Scan(&n, &spent)
	if n != 1 || spent != 0 {
		t.Fatalf("n=%d spent=%v", n, spent)
	}
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1, MaxCalls: 1}, r); !errors.Is(err, budget.ErrIterations) {
		t.Fatalf("free calls must still count against max_iterations: %v", err)
	}
}

func TestRetryAfterHonoured(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"code":429,"message":"rate limited"}}`))
			return
		}
		w.Write([]byte(`{"id":"g","choices":[{"message":{"content":"{\"answer\":\"ok\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0}}`))
	}))
	defer srv.Close()
	_, s, id := setup(t, &gatewaytest.Fake{})
	g := gateway.New(gateway.NewOpenRouter(srv.URL, "k", 5*time.Second), routes, budget.New(s, 10))
	g.SetBackoff(func(int) time.Duration { return time.Millisecond })
	start := time.Now()
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("Retry-After ignored")
	}
}

// A second (OpenAI-compatible) provider takes over when the first fails.
func TestSecondProviderFallback(t *testing.T) {
	or := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Err: &gateway.ProviderError{Status: 402, Msg: "Insufficient credits"}}}}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer second-key" || r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"id":"x","choices":[{"message":{"content":"{\"answer\":\"from second\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()
	second := gateway.NewOpenAICompatible("second", srv.URL+"/v1", "second-key", 5*time.Second)
	two := map[string][]config.ModelRoute{"standard": {
		{DisplayName: "A", APIModelID: "a", RouteID: "route/a", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 100},
		{Provider: "second", DisplayName: "B", APIModelID: "b", RouteID: "b-model", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 100},
	}}
	_, s, id := setup(t, or)
	g := gateway.NewMulti(map[string]gateway.Provider{"openrouter": or, "second": second}, two, budget.New(s, 10))
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, req())
	if err != nil || res.Provider != "second" || string(res.Structured) != `{"answer":"from second"}` {
		t.Fatalf("%v %+v", err, res)
	}
	if _, ok := got["usage"]; ok || got["provider"] != nil || got["model"] != "b-model" {
		t.Fatalf("OpenRouter-only fields sent to a generic provider: %v", got)
	}
	var prov string
	var cost float64
	s.R.QueryRow("select provider_id, cost_actual_usd from usage_records where route_id='b-model'").Scan(&prov, &cost)
	if prov != "second" || cost <= 0 {
		t.Fatalf("usage not attributed: %s %v", prov, cost)
	}
}

func TestInvalidOutputMarkedInUsage(t *testing.T) {
	f := &gatewaytest.Fake{Script: []gatewaytest.Reply{{Content: `nope`}, {Content: `{"answer":"x"}`}}}
	g, s, id := setup(t, f)
	r := req()
	r.Purpose = "verify"
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 1}, r); err != nil {
		t.Fatal(err)
	}
	var invalid, ok int
	s.R.QueryRow("select sum(outcome='invalid_output'), sum(outcome='ok') from usage_records where purpose='verify'").Scan(&invalid, &ok)
	if invalid != 1 || ok != 1 {
		t.Fatalf("invalid=%d ok=%d", invalid, ok)
	}
}

// SELF-07 / OL-03: a route that keeps failing is taken out of rotation, the next
// route serves, and the route is tried again (half-open) after the cooldown.
func TestCircuitBreakerOpensAndRecovers(t *testing.T) {
	var events []string
	var mu sync.Mutex
	down := true
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if r.RouteID == "route/a" && down {
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 429, Msg: "rate limited"}}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	g, _, id := setup(t, f)
	g.Notify = func(ev string, _ map[string]any) { mu.Lock(); events = append(events, ev); mu.Unlock() }
	now := time.Now()
	g.SetClockForTest(func() time.Time { return now })
	call := func() *gateway.Result {
		res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for i := 0; i < 5; i++ {
		if call().RouteID != "route/b" { // always served by the fallback
			t.Fatal("fallback did not serve")
		}
	}
	callsAtOpen := 0
	for _, r := range f.Routes {
		if r == "route/a" {
			callsAtOpen++
		}
	}
	for i := 0; i < 3; i++ {
		call()
	}
	after := 0
	for _, r := range f.Routes {
		if r == "route/a" {
			after++
		}
	}
	if after != callsAtOpen || len(g.OpenRoutes()) != 1 {
		t.Fatalf("open circuit still called route/a: %d -> %d, open=%v", callsAtOpen, after, g.OpenRoutes())
	}
	down = false
	now = now.Add(61 * time.Second) // cooldown over: one probe goes to route/a, which has recovered
	if call().RouteID != "route/a" || len(g.OpenRoutes()) != 0 {
		t.Fatal("route did not recover after the cooldown")
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "R0_CIRCUIT_OPEN,R0_CIRCUIT_CLOSED" {
		t.Fatalf("events %v", events)
	}
}

// OL-01: empty content from a reasoning route is retried once with reasoning
// disabled; OL-02: raw tool-call markup is a failed attempt and falls back.
func TestEmptyAnswerAndToolMarkup(t *testing.T) {
	var seen []string
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		seen = append(seen, r.RouteID+"|"+r.ReasoningEffort)
		switch {
		case r.RouteID == "route/a" && r.ReasoningEffort != gateway.ReasoningOff:
			return gatewaytest.Reply{Content: "   "}
		case r.RouteID == "route/a":
			return gatewaytest.Reply{Content: `{"answer":"after retry"}`}
		}
		return gatewaytest.Reply{Content: `{"answer":"b"}`}
	}}
	g, s, id := setup(t, f)
	withReasoning := map[string][]config.ModelRoute{"standard": {
		{DisplayName: "A", APIModelID: "a", RouteID: "route/a", ReasoningEffort: "medium", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 100},
		{DisplayName: "B", APIModelID: "b", RouteID: "route/b", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 100}}}
	g = gateway.New(f, withReasoning, budget.New(s, 10))
	var events []string
	g.Notify = func(ev string, _ map[string]any) { events = append(events, ev) }
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
	if err != nil || res.RouteID != "route/a" || strings.Join(seen, ",") != "route/a|medium,route/a|none" {
		t.Fatalf("%v %+v %v", err, res, seen)
	}
	if strings.Join(events, ",") != "R0_EMPTY_ANSWER_RETRY" {
		t.Fatal(events)
	}
	var empty int
	s.R.QueryRow("select count(*) from usage_records where outcome='empty_output'").Scan(&empty)
	if empty != 1 {
		t.Fatal("empty answer not recorded")
	}

	seen = nil
	f2 := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if r.RouteID == "route/a" {
			return gatewaytest.Reply{Content: `<tool_call>{"name":"shell","arguments":{"cmd":"id"}}</tool_call>`}
		}
		return gatewaytest.Reply{Content: `{"answer":"b"}`}
	}}
	g2 := gateway.New(f2, withReasoning, budget.New(s, 10))
	res, err = g2.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
	if err == nil && res.RouteID == "route/a" {
		t.Fatal("raw tool-call markup accepted as an answer")
	}
	var markup int
	s.R.QueryRow("select count(*) from usage_records where outcome='tool_call_markup'").Scan(&markup)
	if err != nil || res.RouteID != "route/b" || markup != 1 {
		t.Fatalf("%v %+v markup=%d", err, res, markup)
	}
}

// OL-01 also covers routes that reason by default without a configured effort
// (free reasoning models returning empty content).
func TestEmptyAnswerRetryWithoutConfiguredEffort(t *testing.T) {
	var seen []string
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		seen = append(seen, r.ReasoningEffort)
		if r.ReasoningEffort != gateway.ReasoningOff {
			return gatewaytest.Reply{Content: ""}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	_, s, id := setup(t, f)
	plain := map[string][]config.ModelRoute{"standard": {{DisplayName: "A", APIModelID: "a", RouteID: "route/a", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 100}}}
	g := gateway.New(f, plain, budget.New(s, 10))
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
	if err != nil || res.RouteID != "route/a" || strings.Join(seen, ",") != ",none" {
		t.Fatalf("%v %v", err, seen)
	}
}

// ADR-0054: a route whose provider reports an exhausted quota rests at once,
// so later calls do not pay for the same failure again.
func TestQuotaRestsRouteImmediately(t *testing.T) {
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if r.RouteID == "route/a" {
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 429, Msg: "quota exceeded"}}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	g, _, id := setup(t, f)
	for i := 0; i < 4; i++ {
		res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
		if err != nil || res.RouteID != "route/b" {
			t.Fatalf("call %d: %v %v", i, res, err)
		}
	}
	n := 0
	for _, r := range f.Routes {
		if r == "route/a" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("exhausted route was called %d times, want 1", n)
	}
	var a gateway.RouteHealth
	for _, h := range g.Health() {
		if h.RouteID == "route/a" {
			a = h
		}
	}
	if !a.Resting || a.RestingFor < 50 {
		t.Fatalf("route/a should be resting: %+v", a)
	}
	if hs := g.Health(); hs[0].RouteID != "route/b" {
		t.Fatalf("resting route should be tried last: %+v", hs)
	}
}

// ADR-0054: a route that fails most of its recent calls is tried after the
// healthy ones; old failures are forgotten.
func TestUnhealthyRouteIsDemoted(t *testing.T) {
	bad := true
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if r.RouteID == "route/a" && bad {
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 500, Msg: "boom"}}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	g, _, id := setup(t, f)
	now := time.Now()
	g.SetClockForTest(func() time.Time { return now })
	call := func() string {
		res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
		if err != nil {
			t.Fatal(err)
		}
		return res.RouteID
	}
	for i := 0; i < 4; i++ {
		call()
	}
	before := f.CallCount()
	if call() != "route/b" || f.CallCount() != before+1 {
		t.Fatalf("demoted route was still tried first (%d calls)", f.CallCount()-before)
	}
	bad = false
	now = now.Add(31 * time.Minute) // failures forgotten: configured order again
	if got := call(); got != "route/a" {
		t.Fatalf("route/a should lead again, got %s", got)
	}
}

func tieredRoutes() map[string][]config.ModelRoute {
	return map[string][]config.ModelRoute{
		"standard": {{DisplayName: "A", APIModelID: "a", RouteID: "route/a", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000}},
		"fast":     {{DisplayName: "F", APIModelID: "f", RouteID: "route/f", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000}},
		"deep": {{DisplayName: "L", APIModelID: "l", RouteID: "route/log", Free: true, PromptLogging: "may_log", MaxOutputTokens: 1000},
			{DisplayName: "D", APIModelID: "d", RouteID: "route/d", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000}},
	}
}

// ADR-0054: when the tier has no working route, another tier's route answers.
// Rescue never crosses the privacy rule, and it is off unless enabled.
func TestTierRescue(t *testing.T) {
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if r.RouteID == "route/a" || r.RouteID == "route/d" {
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 503, Msg: "down"}}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	_, s, id := setup(t, f)
	var events []string
	mk := func(rescue bool) *gateway.Gateway {
		g := gateway.New(f, tieredRoutes(), budget.New(s, 10))
		g.SetBackoff(func(int) time.Duration { return time.Millisecond })
		g.Rescue = rescue
		g.Notify = func(ev string, _ map[string]any) { events = append(events, ev) }
		return g
	}
	if _, err := mk(false).Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req()); !errors.Is(err, gateway.ErrAllRoutes) {
		t.Fatalf("without rescue the tier must fail: %v", err)
	}
	f.Routes = nil
	res, err := mk(true).Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
	if err != nil || res.RouteID != "route/f" {
		t.Fatalf("rescue: %+v %v", res, err)
	}
	for _, r := range f.Routes {
		if r == "route/log" {
			t.Fatal("a private task reached a prompt-logging route through rescue")
		}
	}
	if !strings.Contains(strings.Join(events, ","), "R0_TIER_RESCUE") {
		t.Fatalf("rescue not reported: %v", events)
	}
	q := req()
	q.DataClass = "public"
	f.Routes = nil
	if res, err := mk(true).Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, q); err != nil || res.RouteID != "route/log" {
		t.Fatalf("public rescue should use the first deep route: %+v %v", res, err)
	}
}

// ADR-0054: when every route is resting, the one due back soonest is probed
// rather than failing without an attempt.
func TestAllRestingStillProbes(t *testing.T) {
	down := true
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if down {
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 429, Msg: "quota"}}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	g, _, id := setup(t, f)
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req()); !errors.Is(err, gateway.ErrAllRoutes) {
		t.Fatalf("want all routes failed, got %v", err)
	}
	down = false
	before := f.CallCount()
	res, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
	if err != nil || f.CallCount() != before+1 {
		t.Fatalf("resting routes were not probed: %v (%d calls)", err, f.CallCount()-before)
	}
	if res.RouteID != "route/a" {
		t.Fatalf("probe should use the route due back soonest, got %s", res.RouteID)
	}
}

// ADR-0054: a free route that does not answer within the tier's patience is
// abandoned for the next route, without retrying it. A paid route and the last
// candidate are never abandoned.
func TestFreeRoutePatience(t *testing.T) {
	slow := map[string]bool{"route/free1": true}
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if slow[r.RouteID] {
			return gatewaytest.Reply{Content: `{"answer":"late"}`, Delay: 400 * time.Millisecond}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	_, s, id := setup(t, f)
	var events []string
	mk := func(rs []config.ModelRoute) *gateway.Gateway {
		g := gateway.New(f, map[string][]config.ModelRoute{"standard": rs}, budget.New(s, 10))
		g.SetBackoff(func(int) time.Duration { return time.Millisecond })
		g.Patience = map[string]time.Duration{"standard": 40 * time.Millisecond}
		g.Notify = func(ev string, _ map[string]any) { events = append(events, ev) }
		return g
	}
	free1 := config.ModelRoute{APIModelID: "f1", RouteID: "route/free1", Free: true, PromptLogging: "may_log", MaxOutputTokens: 1000}
	free2 := config.ModelRoute{APIModelID: "f2", RouteID: "route/free2", Free: true, PromptLogging: "may_log", MaxOutputTokens: 1000}
	paid := config.ModelRoute{APIModelID: "p", RouteID: "route/paid", PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000}
	q := req()
	q.DataClass = "public"
	b := gateway.Budget{TaskID: id, TaskLimitUSD: 5}

	res, err := mk([]config.ModelRoute{free1, free2}).Complete(context.Background(), b, q)
	if err != nil || res.RouteID != "route/free2" {
		t.Fatalf("the slow free route should be abandoned: %+v %v", res, err)
	}
	if got := strings.Join(f.Routes, ","); got != "route/free1,route/free2" {
		t.Fatalf("the slow route must be tried once and not retried: %s", got)
	}
	if !strings.Contains(strings.Join(events, ","), "R0_SLOW_ROUTE") {
		t.Fatalf("not reported: %v", events)
	}
	// The last candidate is waited for: a late answer beats no answer.
	if res, err := mk([]config.ModelRoute{free1}).Complete(context.Background(), b, q); err != nil || string(res.Structured) != `{"answer":"late"}` {
		t.Fatalf("last candidate: %+v %v", res, err)
	}
	// A paid route is never abandoned for being slow.
	slow = map[string]bool{"route/paid": true}
	if res, err := mk([]config.ModelRoute{paid, free2}).Complete(context.Background(), b, q); err != nil || res.RouteID != "route/paid" {
		t.Fatalf("paid route: %+v %v", res, err)
	}
}

// ADR-0055 addendum: a provider that refuses the key is remembered as an issue
// the owner can act on, announced once, and forgotten when the provider answers.
func TestProviderRefusalIsRememberedUntilItAnswers(t *testing.T) {
	refuse := true
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		if refuse {
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 401, Msg: "User not found."}}
		}
		return gatewaytest.Reply{Content: `{"answer":"ok"}`}
	}}
	g, _, id := setup(t, f)
	var told []map[string]any
	g.Notify = func(ev string, d map[string]any) {
		if ev == "PROVIDER_REFUSED" {
			told = append(told, d)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req()); !errors.Is(err, gateway.ErrAllRoutes) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	is := g.ProviderIssues()
	if len(is) != 1 || is[0].Kind != gateway.IssueKeyRejected || is[0].Status != 401 || is[0].Message != "User not found." {
		t.Fatalf("issues = %+v", is)
	}
	if len(told) != 1 || told[0]["kind"] != gateway.IssueKeyRejected {
		t.Fatalf("the owner must be told exactly once, got %v", told)
	}
	refuse = false
	now := time.Now().Add(11 * time.Minute) // past the rest after a refused key
	g.SetClockForTest(func() time.Time { return now })
	if _, err := g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req()); err != nil {
		t.Fatal(err)
	}
	if is := g.ProviderIssues(); len(is) != 0 {
		t.Fatalf("an answering provider has no issue: %+v", is)
	}
}

// A quota refusal is an issue too, but waiting fixes it: the owner is not notified.
func TestQuotaIssueIsReportedWithoutNotification(t *testing.T) {
	f := &gatewaytest.Fake{Respond: func(r config.ModelRoute, q gateway.Request) gatewaytest.Reply {
		return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 429, Msg: "quota exceeded"}}
	}}
	g, _, id := setup(t, f)
	told := 0
	g.Notify = func(ev string, d map[string]any) {
		if ev == "PROVIDER_REFUSED" {
			told++
		}
	}
	g.Complete(context.Background(), gateway.Budget{TaskID: id, TaskLimitUSD: 5}, req())
	is := g.ProviderIssues()
	if len(is) != 1 || is[0].Kind != gateway.IssueQuota || told != 0 {
		t.Fatalf("issues = %+v, notifications = %d", is, told)
	}
	if d := g.QuotaBackIn(is[0].Provider); d <= 0 || d > 31*time.Minute {
		t.Fatalf("quota back in %v", d)
	}
}
