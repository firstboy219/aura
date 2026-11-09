package task

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/store"
)

type StepState string

const (
	StepPending         StepState = "PENDING"
	StepStarted         StepState = "STARTED"
	StepSucceeded       StepState = "SUCCEEDED"
	StepFailed          StepState = "FAILED"
	StepWaitingApproval StepState = "WAITING_APPROVAL"
)

type Step struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	StepNo         int       `json:"step_no"`
	Description    string    `json:"description"`
	ToolID         *string   `json:"tool_id"`
	SideEffect     string    `json:"side_effect"`
	State          StepState `json:"state"`
	IdempotencyKey string    `json:"idempotency_key"`
	OutcomeRef     *string   `json:"outcome_ref"`
	OutcomeHash    *string   `json:"outcome_hash"`
	Reason         *string   `json:"reason"`
	Attempts       int       `json:"attempts"`
}

// StepKey is the stable idempotency key of a step (Blueprint §8.1).
func StepKey(taskID string, stepNo int) string { return fmt.Sprintf("%s/step/%d", taskID, stepNo) }

// InsertSteps stores the plan's steps in PENDING state.
func InsertSteps(ctx context.Context, tx *sql.Tx, taskID string, steps []Step) error {
	now := ids.NowMs()
	for i := range steps {
		s := &steps[i]
		s.ID, s.TaskID, s.State = ids.New(), taskID, StepPending
		s.IdempotencyKey = StepKey(taskID, s.StepNo)
		if _, err := tx.ExecContext(ctx, `insert into task_steps(id, task_id, step_no, description, tool_id, side_effect,
			state, idempotency_key, created_at, updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
			s.ID, taskID, s.StepNo, s.Description, s.ToolID, s.SideEffect, s.State, s.IdempotencyKey, now, now); err != nil {
			return err
		}
	}
	return nil
}

const stepCols = "id, task_id, step_no, description, tool_id, side_effect, state, idempotency_key, outcome_ref, outcome_hash, reason, attempts"

func scanStep(sc interface{ Scan(...any) error }) (*Step, error) {
	var s Step
	err := sc.Scan(&s.ID, &s.TaskID, &s.StepNo, &s.Description, &s.ToolID, &s.SideEffect, &s.State,
		&s.IdempotencyKey, &s.OutcomeRef, &s.OutcomeHash, &s.Reason, &s.Attempts)
	return &s, err
}

// Steps returns a task's steps in order.
func Steps(ctx context.Context, q store.Querier, taskID string) ([]*Step, error) {
	rows, err := q.QueryContext(ctx, "select "+stepCols+" from task_steps where task_id = ? order by step_no", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Step
	for rows.Next() {
		s, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetStep loads one step.
func GetStep(ctx context.Context, q store.Querier, id string) (*Step, error) {
	s, err := scanStep(q.QueryRowContext(ctx, "select "+stepCols+" from task_steps where id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// UpdateStep sets a step's state and outcome and appends the matching event.
// A STEP_STARTED event counts one attempt.
func UpdateStep(ctx context.Context, tx *sql.Tx, s *Step, state StepState, outcomeRef, outcomeHash, reason string, event string, data map[string]any) error {
	if event == "STEP_STARTED" {
		s.Attempts++
	}
	s.State = state
	if outcomeRef != "" {
		s.OutcomeRef = &outcomeRef
	}
	if outcomeHash != "" {
		s.OutcomeHash = &outcomeHash
	}
	if reason != "" {
		s.Reason = &reason
	}
	_, err := tx.ExecContext(ctx, `update task_steps set state=?, outcome_ref=?, outcome_hash=?, reason=?, attempts=?, updated_at=?
		where id=?`, s.State, s.OutcomeRef, s.OutcomeHash, s.Reason, s.Attempts, ids.NowMs(), s.ID)
	if err != nil || event == "" {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	data["step_no"], data["attempt"], data["idempotency_key"] = s.StepNo, s.Attempts, s.IdempotencyKey
	if reason != "" {
		data["reason"] = reason
	}
	_, err = AppendEvent(ctx, tx, s.TaskID, &s.ID, event, "core", data)
	return err
}
