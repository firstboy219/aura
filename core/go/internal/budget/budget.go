// Package budget is the budget governor (Blueprint §8.4). Before every provider
// call the estimated cost is reserved atomically against the task and daily
// budgets inside one write transaction on the single writer, so concurrent
// callers cannot overspend through stale reads. Unknown cost fails closed: the
// reservation is kept as the spend.
package budget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/store"
)

var (
	ErrTaskBudget  = errors.New("budget_exceeded_task")
	ErrDailyBudget = errors.New("budget_exceeded_daily")
	ErrIterations  = errors.New("budget_exceeded_iterations")
	ErrHalted      = errors.New("halted")
)

// Governor enforces task and global budgets and the kill-switch halt flag.
type Governor struct {
	st        *store.Store
	limitBits atomic.Uint64 // daily limit in USD (float64 bits), changeable at runtime
	halted    atomic.Bool
	now       func() time.Time
	loc       *time.Location // workspace time zone: the daily budget window
}

func New(st *store.Store, dailyLimitUSD float64) *Governor {
	g := &Governor{st: st, now: time.Now, loc: time.UTC}
	g.SetLimit(dailyLimitUSD)
	return g
}

// Halt blocks every new reservation until Resume (kill switch).
func (g *Governor) Halt()          { g.halted.Store(true) }
func (g *Governor) Resume()        { g.halted.Store(false) }
func (g *Governor) Halted() bool   { return g.halted.Load() }
func (g *Governor) Limit() float64 { return math.Float64frombits(g.limitBits.Load()) }

// SetLimit changes the daily limit at runtime (owner admin setting, ADR-0043).
func (g *Governor) SetLimit(usd float64) { g.limitBits.Store(math.Float64bits(usd)) }

// Reservation identifies one reserved provider call.
type Reservation struct {
	UsageID string
	Amount  float64
}

// Call describes a provider call about to be made.
type Call struct {
	TaskID, StepID          string
	Tier, ProviderID, Route string
	APIModelID              string
	EstimateUSD             float64
	TaskLimitUSD            float64
	// RootTaskID / RootLimitUSD bound the whole task tree (subagents spend from
	// the root's budget, ADR-0019). Empty RootTaskID = no tree check.
	RootTaskID   string
	RootLimitUSD float64
	MaxCalls     int    // per-task provider-call limit (max_iterations); 0 = none
	Purpose      string // what the call is for (self-model, shadow routing)
	Free         bool   // zero-price route: a 0 estimate is allowed
}

// SetLocation sets the workspace time zone for the daily budget window (OL-08).
func (g *Governor) SetLocation(loc *time.Location) {
	if loc != nil {
		g.loc = loc
	}
}

// SetClockForTest replaces the clock (tests only).
func (g *Governor) SetClockForTest(now func() time.Time) { g.now = now }

// dayStart is the start of the current workspace-local day in unix ms.
func (g *Governor) dayStart() int64 {
	n := g.now().In(g.loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, g.loc).UnixMilli()
}

const spent = "coalesce(cost_actual_usd, cost_reserved_usd)"

// Reserve atomically checks limits and records a reservation.
func (g *Governor) Reserve(ctx context.Context, c Call) (*Reservation, error) {
	if g.Halted() {
		return nil, ErrHalted
	}
	if c.EstimateUSD < 0 || (c.EstimateUSD == 0 && !c.Free) {
		return nil, fmt.Errorf("budget: estimate must be positive (unknown cost fails closed)")
	}
	r := &Reservation{UsageID: ids.New(), Amount: c.EstimateUSD}
	err := g.st.Write(ctx, func(tx *sql.Tx) error {
		var taskSpent, daySpent float64
		var calls int
		// max_iterations counts logical model calls. A definite rejection (HTTP
		// 4xx/5xx before any work, "rejected") is not one, and neither is an empty
		// answer ("empty_output"): it is always followed by a bounded retry or the
		// next route, which is counted. Their cost still counts against the budget.
		if err := tx.QueryRowContext(ctx, "select coalesce(sum("+spent+"),0), count(*) filter (where outcome not in ('rejected','empty_output')) from usage_records where task_id=?",
			c.TaskID).Scan(&taskSpent, &calls); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "select coalesce(sum("+spent+"),0) from usage_records where created_at>=?",
			g.dayStart()).Scan(&daySpent); err != nil {
			return err
		}
		if c.RootTaskID != "" {
			var treeSpent float64
			if err := tx.QueryRowContext(ctx, "select coalesce(sum("+spent+"),0) from usage_records where task_id in (select id from tasks where root_task_id=?)",
				c.RootTaskID).Scan(&treeSpent); err != nil {
				return err
			}
			if treeSpent+c.EstimateUSD > c.RootLimitUSD {
				return ErrTaskBudget
			}
		}
		if c.MaxCalls > 0 && calls >= c.MaxCalls {
			return ErrIterations
		}
		if taskSpent+c.EstimateUSD > c.TaskLimitUSD {
			return ErrTaskBudget
		}
		if daySpent+c.EstimateUSD > g.Limit() {
			return ErrDailyBudget
		}
		if _, err := tx.ExecContext(ctx, `insert into usage_records(id, task_id, step_id, tier, provider_id, api_model_id, route_id,
			cost_estimated_usd, cost_reserved_usd, outcome, purpose, created_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.UsageID, nullable(c.TaskID), nullable(c.StepID), c.Tier, c.ProviderID, c.APIModelID, c.Route, c.EstimateUSD, c.EstimateUSD,
			"reserved", nullable(c.Purpose), g.now().UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "update tasks set cost_reserved_usd = cost_reserved_usd + ? where id=?", c.EstimateUSD, c.TaskID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Actual is the measured usage of a finished call.
type Actual struct {
	CostUSD                   *float64 // nil when the provider did not report cost and tokens are unknown
	InputTokens, OutputTokens int
	ReasoningTokens           int
	CacheHit                  bool
	LatencyMs                 int64
	RequestID                 string
	Outcome                   string // ok | error | invalid_output | cancelled
}

// Reconcile records actual usage and releases the unused reservation. When the
// cost is unknown, the reservation stays as the spend (fail closed) and the
// record is flagged cost_uncertain.
func (g *Governor) Reconcile(ctx context.Context, r *Reservation, taskID string, a Actual) error {
	ctx = context.WithoutCancel(ctx) // a cancelled call must still be accounted
	return g.st.Write(ctx, func(tx *sql.Tx) error {
		actual := r.Amount
		uncertain := 1
		if a.CostUSD != nil {
			actual, uncertain = *a.CostUSD, 0
		}
		if _, err := tx.ExecContext(ctx, `update usage_records set cost_actual_usd=?, cost_uncertain=?, input_tokens=?, output_tokens=?,
			reasoning_tokens=?, cache_hit=?, latency_ms=?, request_id=?, outcome=? where id=?`,
			actual, uncertain, a.InputTokens, a.OutputTokens, a.ReasoningTokens, boolInt(a.CacheHit), a.LatencyMs,
			nullable(a.RequestID), a.Outcome, r.UsageID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `update tasks set cost_reserved_usd = max(0, cost_reserved_usd - ?),
			cost_actual_usd = cost_actual_usd + ? where id=?`, r.Amount, actual, taskID)
		return err
	})
}

// MarkOutcome overrides a reconciled call's outcome (e.g. invalid_output when
// the provider answered but the content failed schema validation).
func (g *Governor) MarkOutcome(ctx context.Context, usageID, outcome string) error {
	ctx = context.WithoutCancel(ctx)
	return g.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "update usage_records set outcome=? where id=?", outcome, usageID)
		return err
	})
}

// TreeSpent returns the spend (actual, or reserved when unknown) of a whole task tree.
func (g *Governor) TreeSpent(ctx context.Context, rootTaskID string) (float64, error) {
	var s float64
	err := g.st.R.QueryRowContext(ctx, "select coalesce(sum("+spent+"),0) from usage_records where task_id in (select id from tasks where root_task_id=?)", rootTaskID).Scan(&s)
	return s, err
}

// Today returns reserved-but-unreconciled and spent amounts for the current UTC day.
func (g *Governor) Today(ctx context.Context) (reserved, spentUSD float64, err error) {
	err = g.st.R.QueryRowContext(ctx, `select coalesce(sum(case when cost_actual_usd is null then cost_reserved_usd end),0),
		coalesce(sum(cost_actual_usd),0) from usage_records where created_at>=?`, g.dayStart()).Scan(&reserved, &spentUSD)
	return
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
