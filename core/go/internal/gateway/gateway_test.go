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
