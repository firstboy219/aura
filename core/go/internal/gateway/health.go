package gateway

// Adaptive routing (ADR-0054). The gateway keeps a short memory of how each
// route behaved and uses it to decide the order in which routes are tried:
//
//   - a route whose provider said "quota exhausted", "no credit" or "not
//     authorised" is rested at once (circuit open) instead of after five
//     failures, because repeating the call cannot succeed;
//   - a route that failed most of its recent calls is tried after the healthy
//     ones of the same tier;
//   - when every route of the tier failed, routes of the other tiers are tried
//     (tier rescue) so the owner still gets an answer;
//   - when every candidate is resting, the one due back soonest is probed
//     rather than failing without a single attempt.
//
// The privacy rule is applied before any of this: a route that may log prompts
// is never a candidate for a private task, whatever its health.

import (
	"sort"
	"sync"
	"time"

	"aura.local/core/internal/config"
)

const (
	healthWindow    = 8                // outcomes remembered per route
	healthMaxAge    = 30 * time.Minute // older outcomes are forgotten, so a demoted route gets another chance
	healthMinSample = 4
	quotaCooldown   = 60 * time.Second // rest after 429 (or the provider's Retry-After, if longer)
	creditCooldown  = 10 * time.Minute // rest after 401/402/403
	maxQuotaRest    = 30 * time.Minute
)

type outcome struct {
	at time.Time
	ok bool
	ms int64
}

type routeHealth struct {
	mu  sync.Mutex
	m   map[string][]outcome
	now func() time.Time
}

func newRouteHealth() *routeHealth { return &routeHealth{m: map[string][]outcome{}, now: time.Now} }

func (h *routeHealth) record(key string, ok bool, ms int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	o := append(h.m[key], outcome{at: h.now(), ok: ok, ms: ms})
	if len(o) > healthWindow {
		o = o[len(o)-healthWindow:]
	}
	h.m[key] = o
}

// stats returns the recent sample count, success count and mean latency of
// successful calls.
func (h *routeHealth) stats(key string) (n, ok int, ms int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := h.now().Add(-healthMaxAge)
	var sum int64
	for _, o := range h.m[key] {
		if o.at.Before(cut) {
			continue
		}
		n++
		if o.ok {
			ok++
			sum += o.ms
		}
	}
	if ok > 0 {
		ms = sum / int64(ok)
	}
	return n, ok, ms
}

// unhealthy: at least healthMinSample recent calls and fewer than half succeeded.
func (h *routeHealth) unhealthy(key string) bool {
	n, ok, _ := h.stats(key)
	return n >= healthMinSample && ok*2 < n
}

// RouteHealth is one route's recent behaviour (self-model, doctor, API).
type RouteHealth struct {
	Tier       string `json:"tier"`
	Provider   string `json:"provider"`
	RouteID    string `json:"route_id"`
	Samples    int    `json:"samples"`
	Successes  int    `json:"successes"`
	LatencyMs  int64  `json:"latency_ms"`
	Resting    bool   `json:"resting"`
	RestingFor int    `json:"resting_for_s,omitempty"`
	Demoted    bool   `json:"demoted"`
	MayLog     bool   `json:"may_log"`
}

// Health reports every configured route in the order it would be tried now.
func (g *Gateway) Health() []RouteHealth {
	var out []RouteHealth
	for _, tier := range []string{"fast", "standard", "deep"} {
		for _, r := range g.ordered(g.routes[tier]) {
			k := routeKey(r)
			n, ok, ms := g.health.stats(k)
			rest := g.brk.restingFor(k)
			p := r.Provider
			if p == "" {
				p = "openrouter"
			}
			out = append(out, RouteHealth{Tier: tier, Provider: p, RouteID: r.RouteID, Samples: n, Successes: ok, LatencyMs: ms,
				Resting: rest > 0, RestingFor: int(rest.Seconds()), Demoted: g.health.unhealthy(k), MayLog: r.MayLog()})
		}
	}
	return out
}

// ordered returns routes in the order to try: healthy first, then demoted,
// then resting (soonest back first). Configured order breaks ties, so the
// owner's preference holds whenever the routes behave.
func (g *Gateway) ordered(routes []config.ModelRoute) []config.ModelRoute {
	type ranked struct {
		r    config.ModelRoute
		rank int
		rest time.Duration
	}
	rs := make([]ranked, 0, len(routes))
	for _, r := range routes {
		k := routeKey(r)
		x := ranked{r: r, rest: g.brk.restingFor(k)}
		switch {
		case x.rest > 0:
			x.rank = 2
		case g.health.unhealthy(k):
			x.rank = 1
		}
		rs = append(rs, x)
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].rank != rs[j].rank {
			return rs[i].rank < rs[j].rank
		}
		return rs[i].rank == 2 && rs[i].rest < rs[j].rest
	})
	out := make([]config.ModelRoute, len(rs))
	for i, x := range rs {
		out[i] = x.r
	}
	return out
}

// rescueTiers is the order in which other tiers are tried when a tier has no
// working route.
var rescueTiers = map[string][]string{
	"fast":     {"standard", "deep"},
	"standard": {"deep", "fast"},
	"deep":     {"standard", "fast"},
}

// restFor is how long a route rests after a failure that repeating cannot fix
// (0: not such a failure).
func restFor(pe *ProviderError) time.Duration {
	switch pe.Status {
	case 429:
		return min(max(quotaCooldown, pe.RetryAfter), maxQuotaRest)
	case 401, 402, 403:
		return creditCooldown
	}
	return 0
}
