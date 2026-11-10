package gateway

import (
	"testing"

	"aura.local/core/internal/config"
)

// ADR-0054: a route that works but answers far slower than another healthy
// route of the tier is tried after it; without a faster alternative, or with
// too few samples, the configured order holds.
func TestSlowRouteIsTriedLater(t *testing.T) {
	a := config.ModelRoute{RouteID: "a"}
	b := config.ModelRoute{RouteID: "b"}
	c := config.ModelRoute{RouteID: "c"}
	order := func(g *Gateway) string {
		s := ""
		for _, r := range g.ordered([]config.ModelRoute{a, b, c}) {
			s += r.RouteID
		}
		return s
	}
	g := NewMulti(nil, nil, nil)
	g.health.record(routeKey(a), true, 70000)
	if got := order(g); got != "abc" {
		t.Fatalf("one sample must not reorder: %s", got)
	}
	g.health.record(routeKey(a), true, 60000)
	if got := order(g); got != "abc" {
		t.Fatalf("slow with no known faster route keeps its place: %s", got)
	}
	g.health.record(routeKey(b), true, 2000)
	g.health.record(routeKey(b), true, 3000)
	if got := order(g); got != "bca" {
		t.Fatalf("slow route should follow the others: %s", got)
	}
	// Not slow in absolute terms: order is the owner's.
	g2 := NewMulti(nil, nil, nil)
	for _, ms := range []int64{9000, 9000} {
		g2.health.record(routeKey(a), true, ms)
		g2.health.record(routeKey(b), true, ms/9)
	}
	if got := order(g2); got != "abc" {
		t.Fatalf("below the floor nothing moves: %s", got)
	}
}
