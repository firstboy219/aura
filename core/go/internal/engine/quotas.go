package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"aura.local/core/internal/store"
)

// ToolQuotaSetting is the settings row with the owner's per-tool daily quotas (ODI A5, ADR-0074): tool id -> steps per UTC day. No row, no quota: the
// default is unchanged behaviour.
const ToolQuotaSetting = "odi.tool.quotas"

// LoadToolQuotas reads the quotas (missing row: none).
func LoadToolQuotas(ctx context.Context, q store.Querier) (map[string]int, error) {
	out := map[string]int{}
	var v string
	err := q.QueryRowContext(ctx, "select value from settings where key=?", ToolQuotaSetting).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal([]byte(v), &out)
}

// toolQuotaLeft reports whether a tool may run another step today. It counts the tool's steps that started today (UTC), which is what a call is in AURA
// (one tool call per step), not counting the step asking. A quota that cannot be read is not enforced (the quota is a cost control, never an authority).
func (e *Engine) toolQuotaLeft(ctx context.Context, toolID, stepID string) (ok bool, used, limit int) {
	qs, err := LoadToolQuotas(ctx, e.st.R)
	if err != nil || qs[toolID] <= 0 {
		return true, 0, 0
	}
	start := time.Now().UTC().Truncate(24 * time.Hour).UnixMilli()
	e.st.R.QueryRowContext(ctx, "select count(*) from task_steps where tool_id=? and state in ('STARTED','SUCCEEDED','FAILED','WAITING_APPROVAL') and updated_at>=? and id<>?", toolID, start, stepID).Scan(&used)
	return used < qs[toolID], used, qs[toolID]
}
