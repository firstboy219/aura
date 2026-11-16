package engine

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// TriggerPolicy is the optional ODI A7 part of a trigger spec (Blueprint §17): what to do when a slot is missed, when the last run is still going, during quiet
// hours, when the trigger expires, and how to spread load. Every field is optional; no policy means the behaviour AURA always had (a missed slot runs once, never
// a burst of catch-ups).
type TriggerPolicy struct {
	Overlap            string `json:"overlap,omitempty"`              // "" or "allow": start a new run even if the last one is still going; "skip": skip the slot
	QuietFrom          string `json:"quiet_from,omitempty"`           // "22:00" in the workspace time zone; with quiet_to, slots inside the window wait until it ends
	QuietTo            string `json:"quiet_to,omitempty"`             // "06:00" (the window may cross midnight)
	ExpiresAt          int64  `json:"expires_at,omitempty"`           // Unix ms; the trigger disables itself from then on
	MaxRuns            int    `json:"max_runs,omitempty"`             // task triggers only: disable after this many runs
	MissedGraceSeconds int    `json:"missed_grace_seconds,omitempty"` // a slot later than this is skipped instead of run late (0: run it once, as before)
	JitterSeconds      int    `json:"jitter_seconds,omitempty"`       // the next run is delayed by a random 0..N seconds
}

func (p *TriggerPolicy) validate(kind string) error {
	if p == nil {
		return nil
	}
	if p.Overlap != "" && p.Overlap != "allow" && p.Overlap != "skip" {
		return fmt.Errorf("policy.overlap must be allow or skip")
	}
	if (p.QuietFrom == "") != (p.QuietTo == "") {
		return fmt.Errorf("policy.quiet_from and quiet_to go together")
	}
	if p.QuietFrom != "" {
		for _, v := range []string{p.QuietFrom, p.QuietTo} {
			if _, err := time.Parse("15:04", v); err != nil {
				return fmt.Errorf("policy quiet hours look like 22:00")
			}
		}
		if p.QuietFrom == p.QuietTo {
			return fmt.Errorf("policy quiet_from and quiet_to must differ")
		}
	}
	if p.ExpiresAt < 0 || p.MaxRuns < 0 || p.MaxRuns > 100000 || p.MissedGraceSeconds < 0 || p.JitterSeconds < 0 || p.JitterSeconds > 3600 {
		return fmt.Errorf("policy numbers out of range")
	}
	if p.MaxRuns > 0 && kind != "task" {
		return fmt.Errorf("policy.max_runs applies to task triggers")
	}
	return nil
}

// quietEnd says whether now is inside the quiet window and, if so, when the window ends. Pure (unit-tested).
func quietEnd(now time.Time, from, to string, loc *time.Location) (time.Time, bool) {
	if from == "" || to == "" {
		return time.Time{}, false
	}
	f, err1 := time.Parse("15:04", from)
	t, err2 := time.Parse("15:04", to)
	if err1 != nil || err2 != nil {
		return time.Time{}, false
	}
	n := now.In(loc)
	at := func(base time.Time, c time.Time) time.Time {
		return time.Date(base.Year(), base.Month(), base.Day(), c.Hour(), c.Minute(), 0, 0, loc)
	}
	start, end := at(n, f), at(n, t)
	if !end.After(start) { // crosses midnight
		if !n.Before(start) { // evening part: ends tomorrow morning
			return end.Add(24 * time.Hour), true
		}
		if n.Before(end) { // morning part: ends today
			return end, true
		}
		return time.Time{}, false
	}
	if !n.Before(start) && n.Before(end) {
		return end, true
	}
	return time.Time{}, false
}

func jitterFor(p *TriggerPolicy) time.Duration {
	if p == nil || p.JitterSeconds <= 0 {
		return 0
	}
	return time.Duration(rand.Intn(p.JitterSeconds+1)) * time.Second
}

// triggerPolicyGate applies a trigger's policy to its due slot. It returns true when the slot must NOT run (the schedule has then been advanced, deferred or
// the trigger disabled, and the decision audited).
func (e *Engine) triggerPolicyGate(ctx context.Context, tr *Trigger, slot int64) bool {
	p := tr.Spec.Policy
	if p == nil {
		return false
	}
	now := time.Now()
	audit := func(typ, why string) {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, typ, "core", "", "", map[string]any{"trigger": tr.ID, "name": tr.Name, "reason": why, "slot": slot})
		})
	}
	disable := func(typ, why string) bool {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "update triggers set enabled=0, updated_at=? where id=?", ids.NowMs(), tr.ID)
			return err
		})
		audit(typ, why)
		return true
	}
	if p.ExpiresAt > 0 && now.UnixMilli() >= p.ExpiresAt {
		return disable("TRIGGER_EXPIRED", "expires_at reached")
	}
	if p.MaxRuns > 0 {
		var n int
		e.st.R.QueryRowContext(ctx, "select count(*) from tasks where idempotency_key like ?", "trigger:"+tr.ID+":%").Scan(&n)
		if n >= p.MaxRuns {
			return disable("TRIGGER_COMPLETED", fmt.Sprintf("max_runs %d reached", p.MaxRuns))
		}
	}
	if end, quiet := quietEnd(now, p.QuietFrom, p.QuietTo, e.cfg.Location()); quiet {
		e.st.Write(ctx, func(tx *sql.Tx) error { // defer: the slot is not lost, it waits for the end of the quiet window
			_, err := tx.ExecContext(ctx, "update triggers set next_run_at=? where id=? and next_run_at=?", end.UnixMilli(), tr.ID, slot)
			return err
		})
		return true
	}
	advance := func(why string) bool {
		next, _ := nextRun(tr.IntervalSeconds, tr.DailyAt, now, e.cfg.Location())
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "update triggers set next_run_at=? where id=? and next_run_at=?", next.Add(jitterFor(p)).UnixMilli(), tr.ID, slot)
			return err
		})
		audit("TRIGGER_SKIPPED", why)
		return true
	}
	if p.MissedGraceSeconds > 0 && now.UnixMilli()-slot > int64(p.MissedGraceSeconds)*1000 {
		return advance("missed: later than the grace period")
	}
	if p.Overlap == "skip" && tr.LastTaskID != nil {
		var st string
		e.st.R.QueryRowContext(ctx, "select state from tasks where id=?", *tr.LastTaskID).Scan(&st)
		if st != "" && !task.State(st).Terminal() {
			return advance("overlap: the previous run is still going")
		}
	}
	return false
}
