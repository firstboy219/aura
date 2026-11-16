package nexus

import (
	"fmt"
	"sync"
	"time"
)

// State is what AURA believes about NEXUS from objective evidence.
type State string

const (
	Unknown     State = "UNKNOWN" // no recent evidence: fallback is denied (fail closed)
	Healthy     State = "HEALTHY"
	Degraded    State = "DEGRADED"    // some failures, not enough to call NEXUS unavailable: fallback is denied
	Unavailable State = "UNAVAILABLE" // sustained failures of the cognitive path: the only health state that can justify fallback
)

// MonitorConfig holds the PROPOSED thresholds (Blueprint §10.5: "exact thresholds must be proposed after the baseline and approved by the owner").
type MonitorConfig struct {
	FailThreshold    int           // consecutive failed observations that make NEXUS Unavailable
	FailWindow       time.Duration // ...all within this window (a slow trickle of old errors is not sustained failure)
	RecoverSuccesses int           // consecutive good observations needed to leave Unavailable/Degraded
	HoldDown         time.Duration // minimum time in Unavailable before it may flip back (anti-flapping)
	StaleAfter       time.Duration // evidence older than this is no evidence: state is Unknown
}

// DefaultMonitorConfig is the proposal until the owner approves other numbers.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{FailThreshold: 3, FailWindow: 60 * time.Second, RecoverSuccesses: 2, HoldDown: 30 * time.Second, StaleAfter: 45 * time.Second}
}

// Monitor turns probe and call outcomes into a health state with hysteresis. A single transient timeout never makes NEXUS Unavailable.
type Monitor struct {
	cfg      MonitorConfig
	now      func() time.Time
	mu       sync.Mutex
	state    State
	fails    []time.Time
	okRun    int
	lastAt   time.Time
	downAt   time.Time
	seq      int
	evidence string
	OnChange func(from, to State, evidenceRef string) // called without the lock held
}

func NewMonitor(cfg MonitorConfig, now func() time.Time) *Monitor {
	if now == nil {
		now = time.Now
	}
	return &Monitor{cfg: cfg, now: now, state: Unknown}
}

// Observe records one outcome. ok=false must only be used for UnavailabilityEvidence codes.
func (m *Monitor) Observe(ok bool, code, source string) {
	m.mu.Lock()
	now := m.now()
	m.lastAt = now
	m.seq++
	from := m.state
	if ok {
		m.fails = nil
		m.okRun++
		switch m.state {
		case Unknown:
			m.state = Healthy
		case Degraded:
			if m.okRun >= m.cfg.RecoverSuccesses {
				m.state = Healthy
			}
		case Unavailable:
			if m.okRun >= m.cfg.RecoverSuccesses && now.Sub(m.downAt) >= m.cfg.HoldDown {
				m.state = Healthy
			}
		}
	} else {
		m.okRun = 0
		kept := m.fails[:0]
		for _, t := range m.fails {
			if now.Sub(t) <= m.cfg.FailWindow {
				kept = append(kept, t)
			}
		}
		m.fails = append(kept, now)
		switch {
		case len(m.fails) >= m.cfg.FailThreshold:
			if m.state != Unavailable {
				m.downAt = now
			}
			m.state = Unavailable
		case m.state == Healthy || m.state == Unknown:
			m.state = Degraded
		}
	}
	m.evidence = fmt.Sprintf("nexus-health:%d:%s:%s:fails=%d:ok_run=%d", m.seq, source, code, len(m.fails), m.okRun)
	to, ev, cb := m.state, m.evidence, m.OnChange
	m.mu.Unlock()
	if cb != nil && from != to {
		cb(from, to, ev)
	}
}

// State is the current belief; evidence older than StaleAfter makes it Unknown.
func (m *Monitor) State() (State, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != Unknown && m.now().Sub(m.lastAt) > m.cfg.StaleAfter {
		return Unknown, m.evidence + ":stale"
	}
	return m.state, m.evidence
}

// Seed sets the state from an explicit check (the activation gate). The failure window is cleared; the evidence reference names the check.
func (m *Monitor) Seed(to State, source string) {
	m.mu.Lock()
	from := m.state
	m.state, m.fails, m.okRun, m.lastAt = to, nil, m.cfg.RecoverSuccesses, m.now()
	m.seq++
	m.evidence = fmt.Sprintf("nexus-health:%d:%s:seeded:fails=0:ok_run=%d", m.seq, source, m.okRun)
	ev, cb := m.evidence, m.OnChange
	m.mu.Unlock()
	if cb != nil && from != to {
		cb(from, to, ev)
	}
}
