package gateway

// Provider issues (ADR-0055 addendum 2026-11-11). A provider that refuses a call
// with 401/402/403/429 is telling the owner something only the owner can fix (a
// rejected key, no credit) or wait out (quota). The gateway remembers the last
// such refusal per provider until that provider answers again, so a failed task
// can state the real cause instead of a generic "no route".

import (
	"sort"
	"sync"
	"time"
)

// Issue kinds.
const (
	IssueKeyRejected = "key_rejected" // 401, 403: the provider does not accept the configured key
	IssueNoCredit    = "no_credit"    // 402
	IssueQuota       = "quota"        // 429
)

const issueMaxAge = time.Hour // an older refusal says nothing about now

// ProviderIssue is the last definite refusal of a provider since its last answer.
type ProviderIssue struct {
	Provider string    `json:"provider"`
	Kind     string    `json:"kind"`
	Status   int       `json:"status"`
	Message  string    `json:"message"`
	At       time.Time `json:"at"`
}

type providerIssues struct {
	mu sync.Mutex
	m  map[string]ProviderIssue
}

func issueKind(status int) string {
	switch status {
	case 401, 403:
		return IssueKeyRejected
	case 402:
		return IssueNoCredit
	case 429:
		return IssueQuota
	}
	return ""
}

// set records a refusal; changed reports that the provider's issue kind is new
// (the moment worth telling the owner about, once).
func (p *providerIssues) set(provider string, pe *ProviderError, now time.Time) (changed bool) {
	kind := issueKind(pe.Status)
	if kind == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old, had := p.m[provider]
	// A quota refusal on one route does not replace a standing key or credit refusal.
	if had && kind == IssueQuota && old.Kind != IssueQuota && now.Sub(old.At) < issueMaxAge {
		return false
	}
	p.m[provider] = ProviderIssue{Provider: provider, Kind: kind, Status: pe.Status, Message: pe.Msg, At: now}
	return !had || old.Kind != kind || now.Sub(old.At) >= issueMaxAge
}

func (p *providerIssues) clear(provider string) {
	p.mu.Lock()
	delete(p.m, provider)
	p.mu.Unlock()
}

// ProviderIssues lists providers that currently refuse calls, by provider id.
func (g *Gateway) ProviderIssues() []ProviderIssue {
	g.issues.mu.Lock()
	defer g.issues.mu.Unlock()
	var out []ProviderIssue
	for _, i := range g.issues.m {
		if g.brk.now().Sub(i.At) < issueMaxAge {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Provider < out[b].Provider })
	return out
}

// QuotaBackIn is how long until a quota-limited provider's soonest route is due back (0: unknown or now).
func (g *Gateway) QuotaBackIn(provider string) time.Duration {
	var best time.Duration
	for _, tier := range []string{"fast", "standard", "deep"} {
		for _, r := range g.routes[tier] {
			if g.provider(r).ID() != provider {
				continue
			}
			if d := g.brk.restingFor(routeKey(r)); d > 0 && (best == 0 || d < best) {
				best = d
			}
		}
	}
	return best
}
