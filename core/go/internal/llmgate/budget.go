package llmgate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"aura.local/core/internal/store"
)

// BudgetKey is the settings row with JARVIS's monthly AI budget (ADR-0084).
const BudgetKey = "odi.llmgate.jarvis_budget"

// DefaultMonthlyUSD is JARVIS's own budget when AURA took over its keys (2026-11-18: $20 a month).
const DefaultMonthlyUSD = 20.0

// Budget is the owner's cap on what JARVIS's outside-AI calls may cost in a calendar month (workspace time zone).
type Budget struct {
	MonthlyUSD float64 `json:"monthly_usd"`
}

// LoadBudget reads the cap; unset (or unreadable) is the default, never "no cap".
func LoadBudget(ctx context.Context, q store.Querier) Budget {
	var v string
	err := q.QueryRowContext(ctx, "select value from settings where key=?", BudgetKey).Scan(&v)
	b := Budget{MonthlyUSD: DefaultMonthlyUSD}
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return b
	}
	_ = json.Unmarshal([]byte(v), &b)
	return b
}

// SaveBudget stores the cap (owner only, through the admin API).
func SaveBudget(ctx context.Context, tx *sql.Tx, b Budget, now time.Time) error {
	if b.MonthlyUSD < 0 || b.MonthlyUSD > 10000 {
		return errors.New("monthly_usd must be between 0 and 10000 (0 = no cap)")
	}
	raw, _ := json.Marshal(b)
	_, err := tx.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?,?,?)
		on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, BudgetKey, string(raw), now.UnixMilli())
	return err
}
