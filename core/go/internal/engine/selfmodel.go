package engine

// Self-Model v1, efficiency scorecard and shadow routing (ADR-0027). Every
// number here is computed from Core records; nothing is a model's opinion of
// itself. The learned routing policy is only recorded, never applied.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/worker"
)

var tierOrder = []string{"fast", "standard", "deep"}

type tierStats struct {
	Calls        int     `json:"calls"`
	OK           int     `json:"ok"`
	InvalidOut   int     `json:"invalid_output"`
	Errors       int     `json:"errors"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	CostUSD      float64 `json:"cost_usd"`
}

// callStats groups model calls by purpose and tier.
func (e *Engine) callStats(ctx context.Context) (map[string]map[string]*tierStats, error) {
	rows, err := e.st.R.QueryContext(ctx, `select coalesce(purpose,'unknown'), coalesce(tier,''), outcome, count(*), coalesce(avg(latency_ms),0),
		coalesce(sum(coalesce(cost_actual_usd, cost_reserved_usd)),0) from usage_records group by 1,2,3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]*tierStats{}
	for rows.Next() {
		var purpose, tier, outcome string
		var n int
		var lat, cost float64
		rows.Scan(&purpose, &tier, &outcome, &n, &lat, &cost)
		if out[purpose] == nil {
			out[purpose] = map[string]*tierStats{}
		}
		s := out[purpose][tier]
		if s == nil {
			s = &tierStats{}
			out[purpose][tier] = s
		}
		s.AvgLatencyMs = (s.AvgLatencyMs*float64(s.Calls) + lat*float64(n)) / float64(s.Calls+n)
		s.Calls += n
		s.CostUSD += cost
		switch outcome {
		case "ok":
			s.OK += n
		case "invalid_output":
			s.InvalidOut += n
		case "reserved":
		default:
			s.Errors += n
		}
	}
	for _, byTier := range out {
		for _, s := range byTier {
			if s.Calls > 0 {
				s.SuccessRate = float64(s.OK) / float64(s.Calls)
			}
		}
	}
	return out, rows.Err()
}

const shadowPolicyVersion = "v1-cheapest-within-2pp-n20"

var shadowCache struct {
	sync.Mutex
	at    time.Time
	stats map[string]map[string]*tierStats
}

// shadowTier is the learned policy: the cheapest tier whose measured success
// rate for this purpose is within 2 points of the actual tier's, with at least
// 20 samples; otherwise the actual tier.
func (e *Engine) shadowTier(ctx context.Context, purpose, actual string) string {
	shadowCache.Lock()
	if time.Since(shadowCache.at) > time.Minute || shadowCache.stats == nil {
		if s, err := e.callStats(ctx); err == nil {
			shadowCache.stats, shadowCache.at = s, time.Now()
		}
	}
	stats := shadowCache.stats[purpose]
	shadowCache.Unlock()
	cur := stats[actual]
	if cur == nil || cur.Calls < 20 {
		return actual
	}
	for _, t := range tierOrder {
		if t == actual {
			break
		}
		if s := stats[t]; s != nil && s.Calls >= 20 && s.SuccessRate >= cur.SuccessRate-0.02 {
			return t
		}
	}
	return actual
}

func (e *Engine) recordShadow(ctx context.Context, purpose, actual string) {
	if purpose == "" {
		purpose = "unknown"
	}
	shadow := e.shadowTier(ctx, purpose, actual)
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "insert into routing_shadow(id, purpose, actual_tier, shadow_tier, policy_version, created_at) values(?,?,?,?,?,?)",
			ids.New(), purpose, actual, shadow, shadowPolicyVersion, ids.NowMs())
		return err
	})
}

// ShadowReport compares the learned policy with actual routing per purpose.
func (e *Engine) ShadowReport(ctx context.Context) (map[string]any, error) {
	stats, err := e.callStats(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := e.st.R.QueryContext(ctx, "select purpose, actual_tier, shadow_tier, count(*) from routing_shadow group by 1,2,3")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type agg struct{ total, agree, cheaper int }
	by := map[string]*agg{}
	for rows.Next() {
		var p, a, s string
		var n int
		rows.Scan(&p, &a, &s, &n)
		g := by[p]
		if g == nil {
			g = &agg{}
			by[p] = g
		}
		g.total += n
		if a == s {
			g.agree += n
		} else {
			g.cheaper += n
		}
	}
	purposes := map[string]any{}
	for p, g := range by {
		purposes[p] = map[string]any{"decisions": g.total, "agreement_rate": float64(g.agree) / float64(max(g.total, 1)),
			"shadow_prefers_cheaper": g.cheaper, "tiers": stats[p]}
	}
	return map[string]any{"policy": shadowPolicyVersion, "mode": "shadow (recorded only, never applied; promotion needs owner approval)",
		"purposes": purposes}, nil
}

// SelfModel reports measured capabilities and limitations.
func (e *Engine) SelfModel(ctx context.Context) (map[string]any, error) {
	stats, err := e.callStats(ctx)
	if err != nil {
		return nil, err
	}
	tools := map[string]map[string]int{}
	rows, err := e.st.R.QueryContext(ctx, `select type, data_json from task_events where type in ('TOOL_SUCCEEDED','TOOL_FAILED')`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var typ, raw string
		rows.Scan(&typ, &raw)
		var d map[string]any
		json.Unmarshal([]byte(raw), &d)
		tool, _ := d["tool_id"].(string)
		if tools[tool] == nil {
			tools[tool] = map[string]int{}
		}
		if typ == "TOOL_SUCCEEDED" {
			tools[tool]["ok"]++
		} else {
			tools[tool]["failed"]++
		}
	}
	rows.Close()
	var total, succeeded, verified, replanned int
	e.st.R.QueryRowContext(ctx, `select count(*), coalesce(sum(state='COMPLETED'),0), coalesce(sum(replans>0),0) from tasks
		where state in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED')`).Scan(&total, &succeeded, &replanned)
	e.st.R.QueryRowContext(ctx, "select count(distinct task_id) from task_events where type='VERIFIED'").Scan(&verified)
	var limits []string
	if e.srch == nil {
		limits = append(limits, "web.search is not configured")
	}
	if e.charter == "" {
		limits = append(limits, "owner Charter not provided (Blueprint baseline rules apply; GOV-01..05 pending)")
	}
	rr, _ := e.st.R.QueryContext(ctx, `select route_id, sum(outcome='ok'), count(*) from (select route_id, outcome from usage_records
		order by created_at desc limit 200) group by route_id`)
	if rr != nil {
		for rr.Next() {
			var route string
			var ok, n int
			rr.Scan(&route, &ok, &n)
			if n >= 3 && ok == 0 {
				limits = append(limits, "route "+route+" failed its last calls (e.g. no provider credit)")
			}
		}
		rr.Close()
	}
	limits = append(limits, "worker is not OS-sandboxed until P4", "one task runs at a time (ADR-0003)")
	open := e.OpenLimitationsDisclosure(ctx)
	if open == nil {
		open = []string{}
	}
	var openRoutes []string
	if e.gw != nil {
		openRoutes = e.gw.OpenRoutes()
	}
	return map[string]any{
		"open_limitations": open, "routes_in_cooldown": openRoutes,
		"evidence_level": "implementation-verified (computed from this instance's records)",
		"tasks":          map[string]any{"finished": total, "succeeded": succeeded, "verified": verified, "replanned": replanned},
		"models":         stats,
		"tools":          tools,
		"limitations":    limits,
	}, nil
}

// Scorecard is the efficiency scorecard (verified quality joined with cost,
// latency and resources). Energy is unavailable: no trustworthy sensor.
func (e *Engine) Scorecard(ctx context.Context) (map[string]any, error) {
	var walls []float64
	rows, err := e.st.R.QueryContext(ctx, `select (updated_at - coalesce(started_at, created_at)) / 1000.0 from tasks
		where state in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED') and parent_task_id is null`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var w float64
		rows.Scan(&w)
		walls = append(walls, w)
	}
	rows.Close()
	sort.Float64s(walls)
	pct := func(p float64) float64 {
		if len(walls) == 0 {
			return 0
		}
		return walls[min(len(walls)-1, int(p*float64(len(walls)-1)+0.5))]
	}
	var finished, succeeded, verifiedOK int
	var cost, tokIn, tokOut float64
	e.st.R.QueryRowContext(ctx, `select count(*), coalesce(sum(state='COMPLETED'),0) from tasks
		where state in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED') and parent_task_id is null`).Scan(&finished, &succeeded)
	e.st.R.QueryRowContext(ctx, `select count(distinct e.task_id) from task_events e join tasks t on t.id=e.task_id
		where e.type='VERIFIED' and t.state='COMPLETED'`).Scan(&verifiedOK)
	e.st.R.QueryRowContext(ctx, `select coalesce(sum(coalesce(cost_actual_usd, cost_reserved_usd)),0), coalesce(sum(input_tokens),0),
		coalesce(sum(output_tokens),0) from usage_records`).Scan(&cost, &tokIn, &tokOut)
	perSuccess := 0.0
	if succeeded > 0 {
		perSuccess = cost / float64(succeeded)
	}
	_, wpid, _ := e.sup.State()
	return map[string]any{
		"tasks_finished":               finished,
		"tasks_succeeded":              succeeded,
		"verified_success_rate":        float64(verifiedOK) / float64(max(finished, 1)),
		"cost_usd_total":               cost,
		"cost_usd_per_task":            cost / float64(max(finished, 1)),
		"cost_usd_per_successful_task": perSuccess,
		"latency_s":                    map[string]float64{"p50": pct(0.5), "p95": pct(0.95)},
		"tokens":                       map[string]float64{"input": tokIn, "output": tokOut},
		"resources":                    map[string]any{"core_rss_bytes": worker.ProcRSS(os.Getpid()), "worker_rss_bytes": worker.ProcRSS(wpid)},
		"energy":                       "unavailable (no trustworthy sensor; not estimated)",
	}, nil
}
