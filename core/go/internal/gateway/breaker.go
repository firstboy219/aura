package gateway

// R0 runtime self-healing for model routes (Blueprint §8A.5, OL-03): a route
// that keeps failing is taken out of rotation for a cooldown (circuit open),
// tried again with one probe call (half-open), and restored when it answers.
// Every transition is reported to the Notify hook so it lands in the audit log
// and in the Limitation Ledger's evidence.

import (
	"sync"
	"time"
)

const (
	breakerThreshold = 5                // consecutive failures before the circuit opens
	breakerCooldown  = 60 * time.Second // first cooldown; doubles up to breakerMaxCool
	breakerMaxCool   = 15 * time.Minute
)

type breakerState struct {
	failures  int
	openUntil time.Time
	cool      time.Duration
	probing   bool
	forced    bool // one probe admitted before the cooldown ends
}

type breakers struct {
	mu  sync.Mutex
	m   map[string]*breakerState
	now func() time.Time
}

func newBreakers() *breakers { return &breakers{m: map[string]*breakerState{}, now: time.Now} }

// allow reports whether a call may use the route now. In half-open state only
// one probe call is admitted.
func (b *breakers) allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[key]
	if s == nil || s.openUntil.IsZero() {
		return true
	}
	if s.forced {
		s.forced, s.probing = false, true
		return true
	}
	if b.now().Before(s.openUntil) {
		return false
	}
	if s.probing {
		return false
	}
	s.probing = true
	return true
}

// failure records a failed call; it returns true when this failure opened the circuit.
func (b *breakers) failure(key string) (opened bool, cool time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[key]
	if s == nil {
		s = &breakerState{}
		b.m[key] = s
	}
	s.failures++
	wasProbe := s.probing
	s.probing = false
	if s.failures >= breakerThreshold || wasProbe {
		if s.cool == 0 {
			s.cool = breakerCooldown
		} else if wasProbe {
			s.cool = min(s.cool*2, breakerMaxCool)
		}
		s.openUntil = b.now().Add(s.cool)
		return true, s.cool
	}
	return false, 0
}

// rest opens the circuit at once for d (quota exhausted, no credit): repeating
// the call cannot succeed. It returns true when the circuit was closed before.
func (b *breakers) rest(key string, d time.Duration) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[key]
	if s == nil {
		s = &breakerState{}
		b.m[key] = s
	}
	wasOpen := !s.openUntil.IsZero()
	wasProbe := s.probing
	s.probing = false
	s.failures++
	if s.cool == 0 || !wasProbe {
		s.cool = d
	} else {
		s.cool = min(max(s.cool*2, d), breakerMaxCool*2)
	}
	s.openUntil = b.now().Add(s.cool)
	return !wasOpen
}

// restingFor is how much longer the route's circuit stays open (0: usable).
func (b *breakers) restingFor(key string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[key]
	if s == nil || s.openUntil.IsZero() {
		return 0
	}
	return max(s.openUntil.Sub(b.now()), 0)
}

// force admits one probe on an open circuit before its cooldown is over (used
// only when no other route is left).
func (b *breakers) force(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.m[key]; s != nil && !s.probing {
		s.forced = true
	}
}

// success records a good call; it returns true when it closed an open circuit.
func (b *breakers) success(key string) (closed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[key]
	if s == nil {
		return false
	}
	closed = !s.openUntil.IsZero()
	*s = breakerState{}
	return closed
}

// Open lists the routes whose circuit is currently open.
func (b *breakers) open() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k, s := range b.m {
		if !s.openUntil.IsZero() && b.now().Before(s.openUntil) {
			out = append(out, k)
		}
	}
	return out
}
