package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// noAnswer is what the planner is told about a question that timed out under
// on_timeout=continue. It grants nothing; it only allows proceeding on
// assumptions that the output must state (Blueprint §8.2).
const noAnswer = "(The owner did not answer in time. Continue only if the objective can be met without this " +
	"information; state every assumption you make explicitly in the output. If it cannot, write what is missing.)"

// ExpireInputs applies the clarification timeout policy (Blueprint §8.2,
// ADR-0032): one reminder notification after remind_after_seconds, then at
// timeout_seconds either FAILED with INPUT_TIMEOUT or a resume with stated
// assumptions. Clarification never grants permission, so continuing cannot
// widen the task's scope; approvals still apply to every A2 action.
func (e *Engine) ExpireInputs(ctx context.Context) {
	pol := e.cfg.Inputs
	now := ids.NowMs()
	type pending struct {
		id, taskID, question string
		created              int64
		reminded             bool
	}
	rows, err := e.st.R.QueryContext(ctx, "select id, task_id, question, created_at, reminded_at is not null from task_inputs where status='pending'")
	if err != nil {
		return
	}
	var due []pending
	for rows.Next() {
		var p pending
		rows.Scan(&p.id, &p.taskID, &p.question, &p.created, &p.reminded)
		due = append(due, p)
	}
	rows.Close()
	for _, p := range due {
		age := time.Duration(now-p.created) * time.Millisecond
		switch {
		case age >= time.Duration(pol.TimeoutSeconds)*time.Second:
			e.timeOutInput(ctx, p.id, p.taskID, pol.OnTimeout)
		case pol.RemindAfterSeconds > 0 && !p.reminded && age >= time.Duration(pol.RemindAfterSeconds)*time.Second:
			e.remindInput(ctx, p.id, p.taskID, p.question, p.created+int64(pol.TimeoutSeconds)*1000)
		}
	}
}

func (e *Engine) remindInput(ctx context.Context, inputID, taskID, question string, timeoutAt int64) {
	e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update task_inputs set reminded_at=? where id=? and status='pending' and reminded_at is null", ids.NowMs(), inputID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		body, _ := json.Marshal(map[string]any{"input_id": inputID, "question": question, "on_timeout": e.cfg.Inputs.OnTimeout,
			"timeout_at": time.UnixMilli(timeoutAt).UTC().Format(time.RFC3339)})
		if _, err := tx.ExecContext(ctx, "insert into notifications(id, kind, task_id, title, body_json, created_at) values(?,?,?,?,?,?)",
			ids.New(), "info", taskID, "A task is waiting for your answer", string(body), ids.NowMs()); err != nil {
			return err
		}
		_, err = task.AppendEvent(ctx, tx, taskID, nil, "INPUT_REMINDED", "core", map[string]any{"input_id": inputID})
		return err
	})
}

func (e *Engine) timeOutInput(ctx context.Context, inputID, taskID, onTimeout string) {
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update task_inputs set status='timed_out' where id=? and status='pending'", inputID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		t, err := task.Get(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, taskID, nil, "INPUT_TIMEOUT", "core", map[string]any{"input_id": inputID, "policy": onTimeout}); err != nil {
			return err
		}
		if t.State != task.WaitingInput {
			return nil // paused or otherwise moved on; the question simply lapses
		}
		if onTimeout == "continue" {
			var steps int
			tx.QueryRowContext(ctx, "select count(*) from task_steps where task_id=?", taskID).Scan(&steps)
			to := task.Planning
			if steps > 0 {
				to = task.Running
			}
			_, err := task.Transition(ctx, tx, taskID, to, "input_timeout_continue", "", "core")
			return err
		}
		if _, err := task.Transition(ctx, tx, taskID, task.Failed, "input_timeout", "", "core"); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, taskID)
	})
	if err == nil && onTimeout == "continue" {
		e.Wake()
	}
}
