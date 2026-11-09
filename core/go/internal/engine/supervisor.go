package engine

// Persistent Agent Supervisor v1 (ADR-0018): durable waits for an external
// event or a time, woken exactly once by a SQL-only loop (no model calls while
// idle), surviving Core restarts.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/task"
)

const maxWait = 30 * 24 * time.Hour

var (
	eventKeyRe  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)
	errWaitTool = errors.New("wait tools are handled by the supervisor")
)

// createWait parks the task on a wait (idempotent per step) and moves it to WAITING.
func (e *Engine) createWait(ctx context.Context, t *task.Task, st *task.Step, tool string, args map[string]any) (string, error) {
	var id string
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "select id from task_waits where step_id=? and satisfied_at is null", st.ID).Scan(&id); err == nil {
			return nil // already waiting for this step (e.g. re-run after a crash)
		}
		id = ids.New()
		kind, key, reason := "event", "", ""
		var until any
		if tool == "wait.event" {
			key = args["key"].(string)
			reason = "event:" + key
		} else {
			kind = "time"
			until = args["until_ms"].(int64)
			reason = "until:" + args["until"].(string)
		}
		if _, err := tx.ExecContext(ctx, "insert into task_waits(id, task_id, step_id, kind, key, until_at, created_at) values(?,?,?,?,?,?,?)",
			id, t.ID, st.ID, kind, nullable(key), until, ids.NowMs()); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "WAIT_STARTED", "core", map[string]any{"wait_id": id, "kind": kind, "reason": reason}); err != nil {
			return err
		}
		_, err := task.Transition(ctx, tx, t.ID, task.Waiting, "", reason, "core")
		return err
	})
	return id, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Supervise wakes every task whose wait condition is met. It runs only SQL.
func (e *Engine) Supervise(ctx context.Context) int {
	now := ids.NowMs()
	rows, err := e.st.R.QueryContext(ctx, `
		select w.id, w.kind, coalesce(w.key,''), '' from task_waits w where w.satisfied_at is null and w.kind='time' and w.until_at <= ?
		union all
		select w.id, w.kind, w.key, x.id from task_waits w join tasks t on t.id = w.task_id
		  join external_events x on x.key = w.key and x.received_at >= t.created_at
		  where w.satisfied_at is null and w.kind='event'
		union all
		select w.id, w.kind, '', '' from task_waits w where w.satisfied_at is null and w.kind='tasks'
		  and not exists (select 1 from tasks c where c.parent_task_id = w.task_id
		    and c.state not in ('SUCCEEDED','FAILED','CANCELLED','BUDGET_EXCEEDED'))`, now)
	if err != nil {
		e.log.Error("supervisor", "err", err)
		return 0
	}
	type due struct{ id, kind, key, event string }
	var list []due
	for rows.Next() {
		var d due
		rows.Scan(&d.id, &d.kind, &d.key, &d.event)
		list = append(list, d)
	}
	rows.Close()
	woke := 0
	for _, d := range list {
		ok, err := e.wakeWait(ctx, d.id, d.event)
		if err != nil {
			e.log.Error("supervisor wake", "wait", d.id, "err", err)
		}
		if ok {
			woke++
		}
	}
	return woke
}

// wakeWait satisfies a wait exactly once (conditional update in one transaction),
// completes the waiting step with an observation, and resumes the task.
func (e *Engine) wakeWait(ctx context.Context, waitID, eventID string) (bool, error) {
	woke := false
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		by := "time"
		if eventID != "" {
			by = "event:" + eventID
		}
		res, err := tx.ExecContext(ctx, "update task_waits set satisfied_at=?, satisfied_by=? where id=? and satisfied_at is null", ids.NowMs(), by, waitID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil // another wake got there first: exactly once
		}
		woke = true
		var taskID, kind string
		var stepID sql.NullString
		var key sql.NullString
		if err := tx.QueryRowContext(ctx, "select task_id, step_id, kind, key from task_waits where id=?", waitID).Scan(&taskID, &stepID, &kind, &key); err != nil {
			return err
		}
		content := "The wait condition was met."
		source := "supervisor"
		switch kind {
		case "event":
			var payload string
			tx.QueryRowContext(ctx, "select payload_json from external_events where id=?", eventID).Scan(&payload)
			content = fmt.Sprintf("External event %q arrived with payload: %s", key.String, truncate(payload, 4000))
			source = "event:" + key.String
		case "time":
			content = "The waiting time has passed."
		case "tasks":
			content = "All delegated subtasks have finished."
		}
		if stepID.Valid {
			st, err := task.GetStep(ctx, tx, stepID.String)
			if err != nil {
				return err
			}
			ck, err := putCheckpoint(ctx, tx, taskID, st.ID, "observation", obsRecord{Source: source, Content: content})
			if err != nil {
				return err
			}
			if err := task.UpdateStep(ctx, tx, st, task.StepSucceeded, ck, "", "", "STEP_SUCCEEDED", map[string]any{"wait_id": waitID}); err != nil {
				return err
			}
		}
		if _, err := task.AppendEvent(ctx, tx, taskID, nil, "WOKE", "core", map[string]any{"wait_id": waitID, "by": by}); err != nil {
			return err
		}
		cur, err := task.Get(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if cur.State == task.Waiting {
			_, err = task.Transition(ctx, tx, taskID, task.Running, "woke", "", "core")
		}
		return err
	})
	if woke {
		e.Wake()
	}
	return woke, err
}

// ExternalEvent records an event (idempotent by key + dedup id) and nudges the supervisor.
func (e *Engine) ExternalEvent(ctx context.Context, key, dedupID string, payload json.RawMessage) (id string, duplicate bool, err error) {
	if !eventKeyRe.MatchString(key) {
		return "", false, fmt.Errorf("%w: key must match %s", ErrInvalid, eventKeyRe)
	}
	if dedupID == "" {
		dedupID = ids.New()
	}
	if len(dedupID) > 128 || len(payload) > 16<<10 {
		return "", false, fmt.Errorf("%w: dedup_id <= 128 chars, payload <= 16 KiB", ErrInvalid)
	}
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return "", false, fmt.Errorf("%w: payload must be JSON", ErrInvalid)
	}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "select id from external_events where key=? and dedup_id=?", key, dedupID).Scan(&id); err == nil {
			duplicate = true
			return nil
		}
		id = ids.New()
		_, err := tx.ExecContext(ctx, "insert into external_events(id, key, dedup_id, payload_json, received_at) values(?,?,?,?,?)",
			id, key, dedupID, string(payload), ids.NowMs())
		return err
	})
	if err == nil && !duplicate {
		e.Wake()
	}
	return id, duplicate, err
}

// SetPriority changes a task's scheduling priority (-100..100).
func (e *Engine) SetPriority(ctx context.Context, id string, p int) (*task.Task, error) {
	if p < -100 || p > 100 {
		return nil, fmt.Errorf("%w: priority must be -100..100", ErrInvalid)
	}
	if err := e.st.Write(ctx, func(tx *sql.Tx) error { return task.SetPriority(ctx, tx, id, p) }); err != nil {
		return nil, err
	}
	return task.Get(ctx, e.st.R, id)
}
