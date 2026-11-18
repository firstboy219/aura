package nexus

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"aura.local/core/internal/gateway"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
)

// Router implements gateway.CognitionRouter (ADR-0073). Mode off: the legacy path, unchanged. Shadow: legacy serves, NEXUS runs beside it and is measured.
// Primary: NEXUS serves; an external provider is reachable only through Authorizer, and only for the two valid triggers.
type Router struct {
	St          *store.Store
	Client      *Client
	Mon         *Monitor
	Auth        *Authorizer
	ServiceID   string
	WorkspaceID string
	Now         func() time.Time
	// CallTimeout bounds one NEXUS call when the caller's context has no deadline.
	CallTimeout time.Duration

	mu       sync.Mutex
	lastGood *Policy
	// shadowSem bounds the shadow calls in flight: shadow mode measures NEXUS, it must never pile work onto it or onto the host.
	shadowSem chan struct{}
	skips     map[string]int
	// Rand returns a number in [0,100) (tests inject it).
	Rand func() int
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Router) policy(ctx context.Context) Policy {
	p, err := LoadPolicy(ctx, r.St.R)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if r.lastGood != nil {
			return *r.lastGood // a read error must not silently switch the mode
		}
		return DefaultPolicy()
	}
	r.lastGood = &p
	return p
}

// Complete is the gateway's entry point.
func (r *Router) Complete(ctx context.Context, b gateway.Budget, req gateway.Request, legacy func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error)) (*gateway.Result, error) {
	pol := r.policy(ctx)
	switch pol.Mode {
	case ModeShadow:
		res, err := legacy(ctx, b, req)
		if err == nil {
			r.Capture(ctx, pol, req, res)
		}
		if err == nil && pol.BaseURL != "" && r.sampled(pol.ShadowPercent) {
			r.mu.Lock()
			if r.shadowSem == nil {
				r.shadowSem = make(chan struct{}, 2)
			}
			sem := r.shadowSem
			r.mu.Unlock()
			select {
			case sem <- struct{}{}:
				go func() {
					defer func() { <-sem }()
					r.shadow(context.WithoutCancel(ctx), b, req, res)
				}()
			default: // two shadow calls are already running: this one is not measured
			}
		}
		return res, err
	case ModePrimary:
		if len(pol.PrimaryDataClasses) > 0 && !slices.Contains(pol.PrimaryDataClasses, dataClassOf(req)) {
			return legacy(ctx, b, req) // a class the owner keeps on the external providers
		}
		return r.primary(ctx, b, req, pol, legacy)
	}
	return legacy(ctx, b, req)
}

func (r *Router) primary(ctx context.Context, b gateway.Budget, req gateway.Request, pol Policy, legacy func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error)) (*gateway.Result, error) {
	if pol.OwnerDisabledUntil > r.now().UnixMilli() {
		return r.viaFallback(ctx, b, req, legacy, "")
	}
	if st, _ := r.Mon.State(); st == Unavailable {
		return r.viaFallback(ctx, b, req, legacy, "")
	}
	nctx := ctx
	canOverflow := pol.OverflowToExternal && slices.Contains(pol.FallbackDataClass, dataClassOf(req))
	if canOverflow {
		// a heavy call must not hold the task: NEXUS gets OverflowTimeoutSec, then the call is served externally
		var cancel context.CancelFunc
		limit := pol.OverflowTimeoutSec
		if limit <= 0 {
			limit = 45
		}
		nctx, cancel = context.WithTimeout(ctx, time.Duration(limit)*time.Second)
		defer cancel()
	} else if _, has := ctx.Deadline(); !has {
		// nowhere else to go (a private call): NEXUS takes the time it needs, bounded (2026-11-18: a 6k-token plan reads ~10 min cold, ~25 s cached)
		var cancel context.CancelFunc
		nctx, cancel = context.WithTimeout(ctx, pol.PrivateTimeout())
		defer cancel()
	}
	res, err := r.callNexus(nctx, b, req)
	if err == nil {
		r.Mon.Observe(true, "OK", "call")
		return res, nil
	}
	if canOverflow && ctx.Err() == nil && IsCapacityExceeded(err) {
		var ce *Error
		errors.As(err, &ce)
		why := "nexus:" + ce.Code + ":" + ce.Message
		if len(why) > 160 {
			why = why[:160]
		}
		return r.viaFallback(ctx, b, req, legacy, why) // NEXUS is healthy but cannot take this one: not an outage, so the health monitor is not told
	}
	var ne *Error
	if errors.As(err, &ne) && UnavailabilityEvidence(ne.Code) {
		r.Mon.Observe(false, ne.Code, "call")
		if st, _ := r.Mon.State(); st == Unavailable {
			return r.viaFallback(ctx, b, req, legacy, "")
		}
		return nil, fmt.Errorf("%w: %v", ErrNexusUnavailable, err) // one failure is not sustained: retry later, never an external provider
	}
	return nil, err // slow, busy, uncertain, missing knowledge, bad request: NEXUS answers or says so; no external AI
}

func (r *Router) viaFallback(ctx context.Context, b gateway.Budget, req gateway.Request, legacy func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error), overflow string) (*gateway.Result, error) {
	var g *Authorization
	var err error
	if overflow != "" {
		g, err = r.Auth.AuthorizeOverflow(ctx, b.TaskID, req.Purpose, dataClassOf(req), overflow)
	} else {
		g, err = r.Auth.Authorize(ctx, b.TaskID, req.Purpose, dataClassOf(req))
	}
	if err != nil {
		return nil, err
	}
	req.OnlyProviders = g.Providers
	audit := func(res *gateway.Result, err error, class string) {
		r.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
			d := map[string]any{"authorization": g.ID, "purpose": req.Purpose, "trigger": g.Trigger, "ok": err == nil, "route_class": class}
			if res != nil {
				d["provider"], d["route"] = res.Provider, res.RouteID
			}
			return policy.Audit(ctx, tx, "FALLBACK_USED", "core", b.TaskID, g.AuditRef, d)
		})
	}
	// 1) free routes first: no call count, no budget (owner decision: the limits are for paid routes only)
	freeReq := req
	freeReq.RouteClass = "free"
	res, freeErr := legacy(ctx, b, freeReq)
	if freeErr == nil {
		r.Capture(ctx, r.policy(ctx), req, res)
		audit(res, nil, "free")
		return res, nil
	}
	// 2) paid routes: counted against the authorization and capped by its budget
	if err := r.Auth.Use(ctx, g.ID); err != nil {
		return nil, err
	}
	req.RouteClass = "paid"
	if g.BudgetUSD > 0 && (b.TaskLimitUSD == 0 || g.BudgetUSD < b.TaskLimitUSD) {
		b.TaskLimitUSD = g.BudgetUSD
	}
	res, err = legacy(ctx, b, req)
	if err == nil {
		r.Capture(ctx, r.policy(ctx), req, res)
	}
	audit(res, err, "paid")
	return res, err
}

func dataClassOf(req gateway.Request) string {
	if req.DataClass == "public" {
		return "public"
	}
	return "confidential" // the Core marks only explicitly public tasks as public
}

// LLMEnvelope is how a gateway request travels inside the cognitive contract's input.text (adapter v0, ADR-0073).
const LLMEnvelope = "AURA-LLM-REQUEST/1\n"

func (r *Router) build(ctx context.Context, b gateway.Budget, req gateway.Request) Request {
	env, _ := json.Marshal(map[string]any{"tier": req.Tier, "messages": req.Messages, "response_schema": req.ResponseSchema, "max_output_tokens": req.MaxOutputTokens, "purpose": req.Purpose})
	purpose := req.Purpose
	if purpose == "" {
		purpose = "analysis"
	}
	deadline := r.now().Add(120 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl // the caller's time (a private call may take minutes on this CPU), never a fixed 120 s that NEXUS would expire first
	}
	return Request{RequestID: ids.New(), TaskID: orDash(b.TaskID), SessionID: orDash(b.RootTaskID), Caller: Caller{Platform: "aura", ServiceID: r.ServiceID}, Purpose: purpose,
		Scope: Scope{WorkspaceID: r.WorkspaceID, AllowedKnowledge: []string{"kb:approved"}}, Classification: map[bool]string{true: "public", false: "confidential"}[req.DataClass == "public"],
		Deadline: deadline.UTC().Format(time.RFC3339), IdempotencyKey: ids.New(), Input: Input{Text: LLMEnvelope + string(env), EvidenceRefs: []string{}},
		Context: Ctx{ContextID: ids.New(), TokenBudget: 16384}, SchemaVersion: "1.0"}
}

func orDash(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func (r *Router) callNexus(ctx context.Context, b gateway.Budget, req gateway.Request) (*gateway.Result, error) {
	nreq := r.build(ctx, b, req)
	nreq.Priority = priorityOf(ctx)
	cctx := ctx
	if _, ok := ctx.Deadline(); !ok {
		to := r.CallTimeout
		if to <= 0 {
			to = 120 * time.Second
		}
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}
	start := r.now()
	res, err := r.Client.Submit(cctx, nreq)
	ms := r.now().Sub(start).Milliseconds()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = &Error{Code: "DEADLINE_EXCEEDED", Message: "NEXUS did not answer in time"}
		}
		r.usage(ctx, b, req, nil, ms, false)
		return nil, err
	}
	r.usage(ctx, b, req, res, ms, res.Status == "SUCCEEDED")
	switch res.Status {
	case "SUCCEEDED":
	case "INSUFFICIENT_EVIDENCE":
		return nil, fmt.Errorf("%w: %v", ErrInsufficientEvidence, res.Result.Limitations)
	case "CANCELLED", "EXPIRED":
		return nil, &Error{Code: "DEADLINE_EXCEEDED", Message: "request " + res.Status}
	default:
		return nil, &Error{Code: "INTERNAL_ERROR", Message: "NEXUS status " + res.Status}
	}
	out := &gateway.Result{Content: res.Result.Content, Provider: "nexus", RouteID: res.Model.ModelID, APIModelID: res.Model.Version, FinishReason: "stop"}
	if len(req.ResponseSchema) > 0 {
		s, err := gateway.ValidateStructured(req.ResponseSchema, res.Result.Content)
		if err != nil {
			return nil, fmt.Errorf("%w: NEXUS output does not match the requested schema: %v", gateway.ErrInvalidOutput, err)
		}
		out.Structured = s
	}
	return out, nil
}

func (r *Router) usage(ctx context.Context, b gateway.Budget, req gateway.Request, res *Result, ms int64, ok bool) {
	outcome, model, version, reqID := "error", "", "", ""
	if ok {
		outcome = "ok"
	}
	if res != nil {
		model, version, reqID = res.Model.ModelID, res.Model.Version, res.RequestID
	}
	r.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `insert into usage_records(id, task_id, step_id, tier, provider_id, api_model_id, route_id, request_id, cost_estimated_usd, cost_reserved_usd, cost_actual_usd,
			latency_ms, outcome, created_at, purpose) values(?,?,?,?,?,?,?,?,0,0,0,?,?,?,?)`,
			ids.New(), nullable(b.TaskID), nullable(b.StepID), req.Tier, "nexus", version, model, reqID, ms, outcome, r.now().UnixMilli(), nullable(req.Purpose))
		return err // a usage row that cannot be written (e.g. a task that is gone) must never fail the call
	})
}

// shadow runs the same call on NEXUS and records sizes and timing only (never content).
func (r *Router) shadow(ctx context.Context, b gateway.Budget, req gateway.Request, legacy *gateway.Result) {
	ctx, cancel := context.WithTimeout(WithPriority(ctx, "background"), 90*time.Second) // a shadow call is evaluation only: it must never delay the owner
	defer cancel()
	start := r.now()
	res, err := r.callNexus(ctx, b, req)
	ms := r.now().Sub(start).Milliseconds()
	code, nchars := "", 0
	if err != nil {
		code = "ERROR"
		var ne *Error
		if errors.As(err, &ne) {
			code = ne.Code
			if UnavailabilityEvidence(ne.Code) {
				r.Mon.Observe(false, ne.Code, "shadow")
			}
		} else if errors.Is(err, ErrInsufficientEvidence) {
			code = "INSUFFICIENT_EVIDENCE"
		}
	} else {
		nchars = len(res.Content)
		r.Mon.Observe(true, "OK", "shadow")
	}
	r.St.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `insert into nexus_shadow(id, task_id, purpose, ok, error_code, nexus_ms, legacy_provider, nexus_chars, legacy_chars, created_at) values(?,?,?,?,?,?,?,?,?,?)`,
			ids.New(), nullable(b.TaskID), nullable(req.Purpose), btoi(err == nil), nullable(code), ms, legacy.Provider, nchars, len(legacy.Content), r.now().UnixMilli())
		return err
	})
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *Router) sampled(percent int) bool {
	if percent >= 100 {
		return true
	}
	if percent <= 0 {
		return false
	}
	n := 0
	if r.Rand != nil {
		n = r.Rand()
	} else {
		n = int(time.Now().UnixNano()/1000) % 100
	}
	return n < percent
}

// Digest is a short stable id for audit rows that must refer to content without storing it.
func Digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:8]) }
