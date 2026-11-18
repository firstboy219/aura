package app

import (
	"context"
	"database/sql"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/llmgate"
	"aura.local/core/internal/store"
)

// llmGate is JARVIS's way out to an outside AI (ADR-0083): AURA's keys, an allowlist, and one usage row per call ("jarvis:<caller>").
func (a *App) llmGate(st *store.Store) *llmgate.Gate {
	return &llmgate.Gate{Upstreams: llmgate.Default(), Secret: a.secret, Budget: func(ctx context.Context) (float64, float64) {
		limit := llmgate.LoadBudget(ctx, st.R).MonthlyUSD
		now := time.Now().In(a.Cfg.Location())
		from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		var spent float64
		st.R.QueryRowContext(ctx, `select coalesce(sum(cost_actual_usd),0) from usage_records where tier='jarvis' and created_at>=?`, from.UnixMilli()).Scan(&spent)
		return limit, spent
	}, Record: func(ctx context.Context, c llmgate.Call) {
		purpose := "jarvis"
		if c.Caller != "" {
			purpose += ":" + c.Caller
		}
		st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `insert into usage_records(id, tier, provider_id, api_model_id, route_id, request_id, cost_estimated_usd, cost_reserved_usd,
				cost_actual_usd, latency_ms, outcome, created_at, purpose) values(?,?,?,?,?,?,0,0,?,?,?,?,?)`,
				ids.New(), "jarvis", c.Provider, c.Model, c.Path, ids.New(), c.CostUSD, c.LatencyMS, c.Outcome, time.Now().UnixMilli(), purpose)
			return err // a usage row that cannot be written never fails the call
		})
	}}
}
