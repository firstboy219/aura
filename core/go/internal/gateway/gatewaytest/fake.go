// Package gatewaytest provides a scripted fake provider for deterministic tests
// (ADR-0009). It is never wired from configuration.
package gatewaytest

import (
	"context"
	"sync"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
)

// Reply is one scripted provider outcome.
type Reply struct {
	Content string
	Err     error
	Delay   time.Duration
	CostUSD *float64
}

// Fake answers with Respond (if set) or the next scripted reply.
type Fake struct {
	mu      sync.Mutex
	Script  []Reply
	Respond func(route config.ModelRoute, req gateway.Request) Reply
	Calls   []gateway.Request
	Routes  []string
}

func (f *Fake) ID() string { return "openrouter" }

func (f *Fake) Complete(ctx context.Context, route config.ModelRoute, req gateway.Request) (*gateway.Attempt, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.Routes = append(f.Routes, route.RouteID)
	var r Reply
	switch {
	case f.Respond != nil:
		f.mu.Unlock()
		r = f.Respond(route, req)
	case len(f.Script) > 0:
		r, f.Script = f.Script[0], f.Script[1:]
		f.mu.Unlock()
	default:
		f.mu.Unlock()
		r = Reply{Err: &gateway.ProviderError{Status: 500, Transient: false, Msg: "fake: script exhausted"}}
	}
	if r.Delay > 0 {
		select {
		case <-time.After(r.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if r.Err != nil {
		return nil, r.Err
	}
	cost := 0.0001
	if r.CostUSD != nil {
		cost = *r.CostUSD
	}
	return &gateway.Attempt{Content: r.Content, FinishReason: "stop", RequestID: "fake", InputTokens: 100, OutputTokens: 50, CostUSD: &cost}, nil
}

// CallCount returns the number of provider attempts so far.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
