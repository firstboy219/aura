package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

// ODI A7 (ADR-0076): memory expiry, notification snooze/acknowledge, and a dry-run retention report.

// SetMemoryExpiry sets (days>0) or clears (days==0) the expiry of a memory. An expired memory is never retrieved as a fact; the maintenance tick then retires it.
func (e *Engine) SetMemoryExpiry(ctx context.Context, id string, days int) (*Memory, error) {
	if days < 0 || days > 3650 {
		return nil, ErrInvalid
	}
	var exp any
	if days > 0 {
		exp = time.Now().Add(time.Duration(days) * 24 * time.Hour).UnixMilli()
	}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update memories set expires_at=?, updated_at=? where id=? and status in ('active','tentative')", exp, ids.NowMs(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return policy.Audit(ctx, tx, "MEMORY_EXPIRY_SET", "owner", "", "", map[string]any{"memory_id": id, "days": days})
	})
	if err != nil {
		return nil, err
	}
	return e.getMemory(ctx, id)
}

// ExpireMemories retires memories past their expiry: status 'deleted' (the row and its lineage stay; nothing is erased), audited with the count. Called from the
// scheduler tick; SQL only unless something is due.
func (e *Engine) ExpireMemories(ctx context.Context) {
	var n int
	if err := e.st.R.QueryRowContext(ctx, "select count(*) from memories where status in ('active','tentative') and expires_at is not null and expires_at<=?", ids.NowMs()).Scan(&n); err != nil || n == 0 {
		return
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update memories set status='deleted', updated_at=? where status in ('active','tentative') and expires_at is not null and expires_at<=?", ids.NowMs(), ids.NowMs())
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		return policy.Audit(ctx, tx, "MEMORY_EXPIRED", "core", "", "", map[string]any{"count": c})
	})
}

// SnoozeNotification hides a notification until the given number of minutes have passed (1..10080).
func (e *Engine) SnoozeNotification(ctx context.Context, id string, minutes int) error {
	if minutes < 1 || minutes > 10080 {
		return ErrInvalid
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update notifications set snoozed_until=? where id=? and read_at is null", time.Now().Add(time.Duration(minutes)*time.Minute).UnixMilli(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return nil
	})
}

// AckNotification acknowledges a notification: it is read, and the acknowledgement is recorded.
func (e *Engine) AckNotification(ctx context.Context, id string) error {
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update notifications set read_at=coalesce(read_at, ?), acked_at=?, snoozed_until=null where id=? and acked_at is null", ids.NowMs(), ids.NowMs(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return nil
	})
}

// RetentionSetting holds the owner's retention proposal (days per class; 0 or missing: keep forever).
const RetentionSetting = "odi.retention"

// RetentionPolicy is the proposal the report is computed from. Nothing is ever deleted by it: enforcement is a destructive change that needs the owner's explicit
// approval of the numbers, and is not built (ADR-0076).
type RetentionPolicy struct {
	ReadNotificationsDays int  `json:"read_notifications_days"`
	ShellLogDays          int  `json:"shell_log_days"`
	RetiredMemoriesDays   int  `json:"retired_memories_days"`
	ConsumedApprovalsDays int  `json:"consumed_approvals_days"`
	Enforce               bool `json:"enforce"` // false: report only. true: the daily maintenance (and POST /admin/retention/purge) deletes what the numbers allow.
}

// Retention floors: a proposal below these is refused, so a typo can never wipe recent working data. 0 keeps a class forever.
var RetentionFloors = map[string]int{"read_notifications_days": 7, "shell_log_days": 7, "retired_memories_days": 30, "consumed_approvals_days": 30}

// ValidateRetention refuses negative numbers and numbers under the floors (0 = keep forever is allowed).
func ValidateRetention(p RetentionPolicy) error {
	for k, v := range map[string]int{"read_notifications_days": p.ReadNotificationsDays, "shell_log_days": p.ShellLogDays, "retired_memories_days": p.RetiredMemoriesDays, "consumed_approvals_days": p.ConsumedApprovalsDays} {
		if v < 0 || (v > 0 && v < RetentionFloors[k]) {
			return fmt.Errorf("%w: %s must be 0 (keep) or at least %d days", ErrInvalid, k, RetentionFloors[k])
		}
	}
	return nil
}

// PurgeRetention deletes what the owner's numbers allow, when the policy says Enforce. It never touches the audit log, task events, tasks, or an approval/memory that something else still points to
// (a superseded memory that a newer one supersedes stays). Returns what was deleted per class; one audit entry names the counts, never the content.
func (e *Engine) PurgeRetention(ctx context.Context) (map[string]int64, error) {
	var p RetentionPolicy
	var v string
	if err := e.st.R.QueryRowContext(ctx, "select value from settings where key=?", RetentionSetting).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if json.Unmarshal([]byte(v), &p) != nil || !p.Enforce {
		return nil, nil
	}
	if err := ValidateRetention(p); err != nil {
		return nil, err
	}
	cut := func(days int) int64 { return time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli() }
	out := map[string]int64{"read_notifications": 0, "shell_log": 0, "retired_memories": 0, "consumed_approvals": 0}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		run := func(key string, days int, q string) error {
			if days <= 0 {
				return nil
			}
			res, err := tx.ExecContext(ctx, q, cut(days))
			if err != nil {
				return err
			}
			out[key], _ = res.RowsAffected()
			return nil
		}
		if err := run("read_notifications", p.ReadNotificationsDays, "delete from notifications where read_at is not null and read_at<?"); err != nil {
			return err
		}
		if err := run("shell_log", p.ShellLogDays, "delete from shell_log where finished_at is not null and finished_at<?"); err != nil {
			return err
		}
		if err := run("retired_memories", p.RetiredMemoriesDays, "delete from memories where status in ('deleted','superseded') and updated_at<? and id not in (select supersedes from memories where supersedes is not null)"); err != nil {
			return err
		}
		if err := run("consumed_approvals", p.ConsumedApprovalsDays, "delete from approvals where status in ('consumed','expired','invalidated') and created_at<?"); err != nil {
			return err
		}
		var total int64
		for _, n := range out {
			total += n
		}
		if total == 0 {
			return nil
		}
		return policy.Audit(ctx, tx, "RETENTION_PURGED", "system", "", "", map[string]any{"deleted": out, "policy": p})
	})
	return out, err
}

// RetentionReport counts what the proposal WOULD purge. The audit log and task events are not part of any proposal (they are the evidence).
func (e *Engine) RetentionReport(ctx context.Context, q store.Querier) (RetentionPolicy, map[string]int, error) {
	var p RetentionPolicy
	var v string
	if err := q.QueryRowContext(ctx, "select value from settings where key=?", RetentionSetting).Scan(&v); err == nil {
		json.Unmarshal([]byte(v), &p)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return p, nil, err
	}
	cut := func(days int) int64 { return time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli() }
	out := map[string]int{"read_notifications": 0, "shell_log": 0, "retired_memories": 0, "consumed_approvals": 0}
	count := func(key string, days int, sqlq string) {
		if days > 0 {
			var n int
			q.QueryRowContext(ctx, sqlq, cut(days)).Scan(&n)
			out[key] = n
		}
	}
	count("read_notifications", p.ReadNotificationsDays, "select count(*) from notifications where read_at is not null and read_at<?")
	count("shell_log", p.ShellLogDays, "select count(*) from shell_log where finished_at is not null and finished_at<?")
	count("retired_memories", p.RetiredMemoriesDays, "select count(*) from memories where status in ('deleted','superseded') and updated_at<?")
	count("consumed_approvals", p.ConsumedApprovalsDays, "select count(*) from approvals where status in ('consumed','expired','invalidated') and created_at<?")
	return p, out, nil
}
