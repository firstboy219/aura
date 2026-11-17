package nexus

import (
	"context"
	"time"

	"aura.local/core/internal/gateway"
	"aura.local/core/internal/store"
)

// Service wires the pieces the app and the admin API use.
type Service struct {
	Router  *Router
	Mon     *Monitor
	Auth    *Authorizer
	Teacher *Teacher
	Client  *Client
	St      *store.Store
	// ProbeEvery is the bounded active probe interval (Blueprint §10.5).
	ProbeEvery time.Duration
	Log        func(msg string, kv ...any)
}

// NewService builds the service. legacy is the gateway's original path; token and serviceID identify AURA to NEXUS.
func NewService(st *store.Store, legacy func(context.Context, gateway.Budget, gateway.Request) (*gateway.Result, error), token func() string, serviceID, workspaceID string, cfg MonitorConfig) *Service {
	mon := NewMonitor(cfg, nil)
	var svc *Service
	client := &Client{Token: token}
	auth := &Authorizer{St: st, Mon: mon}
	router := &Router{St: st, Client: client, Mon: mon, Auth: auth, ServiceID: serviceID, WorkspaceID: workspaceID}
	svc = &Service{Router: router, Mon: mon, Auth: auth, Client: client, St: st, ProbeEvery: 15 * time.Second,
		Teacher: &Teacher{St: st, Legacy: legacy}}
	client.BaseURL = func() string {
		p, _ := LoadPolicy(context.Background(), st.R)
		return p.BaseURL
	}
	mon.OnChange = func(from, to State, ev string) {
		if svc.Log != nil {
			svc.Log("nexus health changed", "from", string(from), "to", string(to), "evidence", ev)
		}
		if from == Unavailable && to == Healthy { // recovery: stop fallback, return cognition to NEXUS (Blueprint §7)
			auth.RevokeAll(context.Background(), "NEXUS_RECOVERED")
		}
	}
	return svc
}

// Run probes NEXUS while the mode is not off, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.ProbeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ProbeOnce(ctx)
			if time.Now().Minute()%2 == 0 { // every other minute at most; PushLessons does nothing unless the owner enabled teaching
				s.PushLessons(ctx)
			}
		}
	}
}

// ProbeOnce runs one bounded readiness probe and feeds the monitor.
func (s *Service) ProbeOnce(ctx context.Context) {
	p, err := LoadPolicy(ctx, s.St.R)
	if err != nil || p.Mode == ModeOff || p.BaseURL == "" {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Client.Ready(pctx); err != nil {
		code := "TRANSPORT"
		if ne, ok := err.(*Error); ok {
			code = ne.Code
		}
		if UnavailabilityEvidence(code) || pctx.Err() != nil {
			s.Mon.Observe(false, code, "probe")
		}
		return
	}
	s.Mon.Observe(true, "OK", "probe")
}

// ActivationCheck is the gate before NEXUS becomes primary: the candidate URL must answer RecoverSuccesses consecutive readiness probes now. It uses its own
// client, so a wrong URL or a NEXUS that is still starting never becomes "evidence" in the monitor that AURA's fallback decisions rest on. On success the
// monitor is seeded Healthy (the evidence is these probes).
func (s *Service) ActivationCheck(ctx context.Context, baseURL string) error {
	c := &Client{BaseURL: func() string { return baseURL }, Token: s.Client.Token, HTTP: s.Client.HTTP}
	need := s.Mon.cfg.RecoverSuccesses
	if need < 1 {
		need = 1
	}
	var last error
	for i := 0; i < need+2; i++ {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		last = c.Ready(pctx)
		cancel()
		if last == nil {
			need--
			if need == 0 {
				s.Mon.Seed(Healthy, "activation-check")
				return nil
			}
			continue
		}
	}
	return last
}
