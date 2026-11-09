// Package gateway is the Model Gateway (Blueprint §8.5). It owns provider
// credentials, maps logical tiers to verified routes, reserves budget before
// every call, retries transient failures with bounded backoff, falls back among
// configured OpenRouter routes, validates structured output against the
// requested JSON Schema with at most one repair attempt, and records usage.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a provider-neutral completion request by logical tier.
type Request struct {
	Tier            string          `json:"tier"`
	Messages        []Message       `json:"messages"`
	ResponseSchema  json.RawMessage `json:"response_schema,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	// Purpose labels the call (plan, review, verify, write, replan, pick_url,
	// calc, reflect) for the self-model and shadow routing (ADR-0027).
	Purpose string `json:"purpose,omitempty"`
	// DataClass is set by the Core from the task ("public" or "private"); the
	// worker cannot set it. Routes that may log prompts serve public tasks only.
	DataClass string `json:"-"`
}

// Result of one completion (after any repair).
type Result struct {
	Content      string          `json:"content"`
	Structured   json.RawMessage `json:"structured,omitempty"`
	Provider     string          `json:"provider"`
	RouteID      string          `json:"route_id"`
	APIModelID   string          `json:"api_model_id"`
	FinishReason string          `json:"finish_reason"`
}

// Attempt is what a provider adapter returns for a single HTTP attempt.
type Attempt struct {
	Content         string
	FinishReason    string
	RequestID       string
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int
	CostUSD         *float64
	CacheHit        bool
	usageID         string // set by the gateway after accounting
}

// Provider is one adapter (OpenRouter in P1). It performs exactly one attempt.
type Provider interface {
	ID() string
	Complete(ctx context.Context, route config.ModelRoute, req Request) (*Attempt, error)
}

// ProviderError classifies adapter failures.
type ProviderError struct {
	Status     int
	Transient  bool
	Msg        string
	RetryAfter time.Duration // from the provider's Retry-After header, if any
}

func (e *ProviderError) Error() string { return fmt.Sprintf("provider error %d: %s", e.Status, e.Msg) }

var (
	ErrNoRoute       = errors.New("no_route_for_tier")
	ErrInvalidOutput = errors.New("invalid_model_output")
	ErrAllRoutes     = errors.New("all_routes_failed")
)

// Gateway routes requests by tier.
type Gateway struct {
	provs      map[string]Provider
	routes     map[string][]config.ModelRoute
	gov        *budget.Governor
	maxRetries int
	backoff    func(attempt int) time.Duration
	brk        *breakers
	// Notify receives R0 self-healing events: R0_CIRCUIT_OPEN, R0_CIRCUIT_CLOSED,
	// R0_EMPTY_ANSWER_RETRY, R0_TOOL_CALL_MARKUP (may be nil).
	Notify func(event string, data map[string]any)
}

func (g *Gateway) notify(event string, data map[string]any) {
	if g.Notify != nil {
		g.Notify(event, data)
	}
}

// OpenRoutes lists routes whose circuit breaker is open.
func (g *Gateway) OpenRoutes() []string { return g.brk.open() }

// New creates a gateway whose routes use one provider (the default "openrouter" id).
func New(p Provider, routes map[string][]config.ModelRoute, gov *budget.Governor) *Gateway {
	return NewMulti(map[string]Provider{"openrouter": p}, routes, gov)
}

// NewMulti creates a gateway over several providers; each route names its
// provider (default "openrouter"), so tier fallback can cross providers.
func NewMulti(provs map[string]Provider, routes map[string][]config.ModelRoute, gov *budget.Governor) *Gateway {
	return &Gateway{provs: provs, routes: routes, gov: gov, maxRetries: 2, brk: newBreakers(),
		backoff: func(a int) time.Duration { return time.Duration(500*(1<<a)) * time.Millisecond }}
}

// SetBackoff replaces the retry backoff (tests).
func (g *Gateway) SetBackoff(f func(int) time.Duration) { g.backoff = f }

// Routes returns the configured routes for a tier.
func (g *Gateway) Routes(tier string) []config.ModelRoute { return g.routes[tier] }

// Budget scopes one call to a task.
type Budget struct {
	TaskID, StepID string
	TaskLimitUSD   float64
	RootTaskID     string
	RootLimitUSD   float64
	MaxCalls       int
}

// Complete runs a request: per route, reserve → attempt (with bounded retry) →
// reconcile; structured output is validated and repaired at most once.
func (g *Gateway) Complete(ctx context.Context, b Budget, req Request) (*Result, error) {
	var routes []config.ModelRoute
	for _, r := range g.routes[req.Tier] {
		if r.MayLog() && req.DataClass != "public" {
			continue // never send private task content to an endpoint that may log prompts
		}
		routes = append(routes, r)
	}
	if len(routes) == 0 {
		if len(g.routes[req.Tier]) > 0 {
			return nil, fmt.Errorf("%w: %s has only routes that may log prompts; mark the task public to use them", ErrNoRoute, req.Tier)
		}
		return nil, fmt.Errorf("%w: %s", ErrNoRoute, req.Tier)
	}
	var schema *jsonschema.Schema
	if len(req.ResponseSchema) > 0 {
		var err error
		if schema, err = compileSchema(req.ResponseSchema); err != nil {
			return nil, fmt.Errorf("gateway: response_schema: %w", err)
		}
	}
	var lastErr error
	for _, route := range routes {
		res, err := g.onRoute(ctx, b, route, req, schema)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !fallbackable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrAllRoutes, lastErr)
}

// fallbackable: provider failures move to the next route; budget, halt,
// cancellation and invalid output do not.
func fallbackable(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe)
}

var errCircuitOpen = &ProviderError{Status: 0, Transient: false, Msg: "route circuit is open (recent repeated failures)"}

func routeKey(route config.ModelRoute) string { return route.Provider + "|" + route.RouteID }

func (g *Gateway) onRoute(ctx context.Context, b Budget, route config.ModelRoute, req Request, schema *jsonschema.Schema) (*Result, error) {
	if !g.brk.allow(routeKey(route)) {
		return nil, errCircuitOpen
	}
	msgs := append([]Message(nil), req.Messages...)
	if schema != nil && route.StructuredOutput == "prompt" {
		msgs = append(msgs, Message{Role: "system", Content: "Reply with only one JSON object, no prose and no code fence, " +
			"that validates against this JSON Schema:\n" + string(req.ResponseSchema)})
	}
	for repair := 0; repair <= 1; repair++ {
		r := req
		r.Messages = msgs
		at, err := g.attempt(ctx, b, route, r)
		if err != nil {
			return nil, err
		}
		res := &Result{Content: at.Content, Provider: g.provider(route).ID(), RouteID: route.RouteID, APIModelID: route.APIModelID,
			FinishReason: at.FinishReason}
		if schema == nil {
			return res, nil
		}
		structured, verr := validate(schema, at.Content)
		if verr == nil {
			res.Structured = structured
			return res, nil
		}
		g.gov.MarkOutcome(ctx, at.usageID, "invalid_output")
		if repair == 1 {
			return nil, fmt.Errorf("%w: %s", ErrInvalidOutput, truncate(verr.Error(), 300))
		}
		msgs = append(msgs, Message{Role: "assistant", Content: truncate(at.Content, 8000)},
			Message{Role: "user", Content: "Your previous reply did not match the required JSON schema: " +
				truncate(verr.Error(), 1000) + "\nReply again with only a JSON object that matches the schema."})
	}
	panic("unreachable")
}

// attempt reserves budget, calls the provider with bounded retry on transient
// errors, and reconciles every attempt.
func (g *Gateway) attempt(ctx context.Context, b Budget, route config.ModelRoute, req Request) (*Attempt, error) {
	maxOut := req.MaxOutputTokens
	if maxOut <= 0 || (route.MaxOutputTokens > 0 && maxOut > route.MaxOutputTokens) {
		maxOut = route.MaxOutputTokens
	}
	if maxOut <= 0 {
		maxOut = 4096
	}
	req.MaxOutputTokens = maxOut
	est := Estimate(route, req)
	for try := 0; ; try++ {
		res, err := g.gov.Reserve(ctx, budget.Call{TaskID: b.TaskID, StepID: b.StepID, Tier: req.Tier, ProviderID: g.provider(route).ID(),
			Route: route.RouteID, APIModelID: route.APIModelID, EstimateUSD: est, TaskLimitUSD: b.TaskLimitUSD, RootTaskID: b.RootTaskID, RootLimitUSD: b.RootLimitUSD, MaxCalls: b.MaxCalls,
			Free: route.Free, Purpose: req.Purpose})
		if err != nil {
			return nil, err
		}
		start := time.Now()
		at, err := g.provider(route).Complete(ctx, route, req)
		lat := time.Since(start).Milliseconds()
		act := budget.Actual{LatencyMs: lat, Outcome: "ok"}
		if at != nil {
			act.InputTokens, act.OutputTokens, act.ReasoningTokens = at.InputTokens, at.OutputTokens, at.ReasoningTokens
			act.CacheHit, act.RequestID, act.CostUSD = at.CacheHit, at.RequestID, at.CostUSD
			if act.CostUSD == nil && (at.InputTokens > 0 || at.OutputTokens > 0) {
				c := (float64(at.InputTokens)*route.PriceInPerMTok + float64(at.OutputTokens+at.ReasoningTokens)*route.PriceOutPerMTok) / 1e6
				act.CostUSD = &c
			}
		}
		if err != nil {
			act.Outcome = "error"
			if ctx.Err() != nil {
				act.Outcome = "cancelled"
			}
			// A definite HTTP error response means the provider did not run the
			// request, so nothing was spent. Transport failures and timeouts stay
			// uncertain and keep the reservation as spend (fail closed).
			var pe *ProviderError
			if errors.As(err, &pe) && pe.Status >= 400 && act.CostUSD == nil {
				zero := 0.0
				act.CostUSD = &zero
			}
		}
		// OL-01: a reasoning model may answer with empty content. Retry once with
		// reasoning disabled (explicit setting, recorded); OL-02: raw tool-call
		// markup is a failed attempt, never an answer.
		var contentErr *ProviderError
		if err == nil {
			switch {
			case strings.TrimSpace(at.Content) == "":
				act.Outcome = "empty_output"
				contentErr = &ProviderError{Status: 0, Transient: false, Msg: "empty_output"}
			case rawToolMarkup(at.Content):
				act.Outcome = "tool_call_markup"
				contentErr = &ProviderError{Status: 0, Transient: false, Msg: "raw_tool_call_markup"}
				g.notify("R0_TOOL_CALL_MARKUP", map[string]any{"route": route.RouteID, "provider": route.Provider})
			}
		}
		if rerr := g.gov.Reconcile(ctx, res, b.TaskID, act); rerr != nil {
			return nil, rerr
		}
		if contentErr != nil {
			if contentErr.Msg == "empty_output" && route.ReasoningEffort != "" && !emptyRetried(ctx) {
				g.notify("R0_EMPTY_ANSWER_RETRY", map[string]any{"route": route.RouteID, "reason": "retrying once with reasoning disabled"})
				route.ReasoningEffort = ""
				ctx = context.WithValue(ctx, emptyRetryKey{}, true)
				continue
			}
			g.failRoute(route, contentErr)
			return nil, contentErr
		}
		if err == nil {
			g.okRoute(route)
			at.usageID = res.UsageID
			return at, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var pe *ProviderError
		if !errors.As(err, &pe) {
			pe = &ProviderError{Status: 0, Transient: true, Msg: err.Error()}
		}
		if !pe.Transient || try >= g.maxRetries {
			g.failRoute(route, pe)
			return nil, pe
		}
		wait := g.backoff(try)
		if pe.RetryAfter > wait {
			wait = min(pe.RetryAfter, 20*time.Second)
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// missingProvider fails every call for a route whose provider is not configured.
type missingProvider string

func (m missingProvider) ID() string { return string(m) }
func (m missingProvider) Complete(context.Context, config.ModelRoute, Request) (*Attempt, error) {
	return nil, &ProviderError{Status: 0, Transient: false, Msg: "provider " + string(m) + " is not configured"}
}

func (g *Gateway) provider(route config.ModelRoute) Provider {
	id := route.Provider
	if id == "" {
		id = "openrouter"
	}
	if p, ok := g.provs[id]; ok {
		return p
	}
	return missingProvider(id)
}

// Estimate is a conservative upper bound: ~3 characters per input token plus
// the full output allowance at the route's prices.
func Estimate(route config.ModelRoute, req Request) float64 {
	chars := len(req.ResponseSchema)
	for _, m := range req.Messages {
		chars += len(m.Content) + 16
	}
	in := float64(chars)/3 + 64
	return (in*route.PriceInPerMTok + float64(req.MaxOutputTokens)*route.PriceOutPerMTok) / 1e6
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://response.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("mem://response.json")
}

// validate parses model content as a single JSON value (tolerating a fenced
// code block) and validates it against the schema.
func validate(s *jsonschema.Schema, content string) (json.RawMessage, error) {
	c := strings.TrimSpace(content)
	if strings.HasPrefix(c, "```") {
		c = strings.TrimPrefix(c, "```json")
		c = strings.TrimPrefix(c, "```")
		c = strings.TrimSuffix(strings.TrimSpace(c), "```")
	}
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(c))
	if err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	if err := s.Validate(v); err != nil {
		return nil, err
	}
	out, _ := json.Marshal(v)
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type emptyRetryKey struct{}

func emptyRetried(ctx context.Context) bool { v, _ := ctx.Value(emptyRetryKey{}).(bool); return v }

var markupRe = regexp.MustCompile(`(?i)<\s*(tool_call|function_call|function_calls|tool_use)\b|<\|(tool|python_tag)|\[TOOL_CALLS?\]`)

// rawToolMarkup detects tool calls returned as raw text markup (OL-02).
func rawToolMarkup(s string) bool { return markupRe.MatchString(s) }

func (g *Gateway) failRoute(route config.ModelRoute, pe *ProviderError) {
	if opened, cool := g.brk.failure(routeKey(route)); opened {
		g.notify("R0_CIRCUIT_OPEN", map[string]any{"route": route.RouteID, "provider": route.Provider, "cooldown_s": int(cool.Seconds()),
			"last_error": pe.Msg, "status": pe.Status})
	}
}

func (g *Gateway) okRoute(route config.ModelRoute) {
	if g.brk.success(routeKey(route)) {
		g.notify("R0_CIRCUIT_CLOSED", map[string]any{"route": route.RouteID, "provider": route.Provider})
	}
}

// SetClockForTest replaces the circuit breaker clock (tests only).
func (g *Gateway) SetClockForTest(now func() time.Time) { g.brk.now = now }
