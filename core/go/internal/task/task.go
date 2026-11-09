// Package task holds the task state machine, the append-only event log and the
// task/step projections (Blueprint §8.1, §8.2). Every state change appends an
// event in the same transaction that updates the projection.
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/obs"
	"aura.local/core/internal/store"
)

type State string

const (
	Created         State = "CREATED"
	Planning        State = "PLANNING"
	Running         State = "RUNNING"
	WaitingApproval State = "WAITING_APPROVAL"
	Paused          State = "PAUSED"
	Succeeded       State = "SUCCEEDED"
	Failed          State = "FAILED"
	Cancelled       State = "CANCELLED"
	BudgetExceeded  State = "BUDGET_EXCEEDED"
)

var transitions = map[State][]State{
	Created:         {Planning, Cancelled, Paused, Failed},
	Planning:        {Running, Failed, Cancelled, Paused, BudgetExceeded},
	Running:         {WaitingApproval, Succeeded, Failed, Cancelled, Paused, BudgetExceeded},
	WaitingApproval: {Running, Failed, Cancelled, Paused},
	Paused:          {Created, Planning, Running, WaitingApproval, Cancelled},
}

// Terminal reports whether no further transition is possible.
func (s State) Terminal() bool {
	return s == Succeeded || s == Failed || s == Cancelled || s == BudgetExceeded
}

// CanTransition reports whether from → to is legal.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ErrInvalidTransition is returned for an illegal state change.
var ErrInvalidTransition = errors.New("invalid_transition")

// ErrNotFound is returned when a task or step does not exist.
var ErrNotFound = errors.New("not_found")

type Budget struct {
	MaxCostUSD     float64 `json:"max_cost_usd"`
	MaxIterations  int     `json:"max_iterations"`
	MaxWallSeconds int     `json:"max_wall_seconds"`
}

type Scope struct {
	Tools        []string `json:"tools"`
	AllowDomains []string `json:"allow_domains"`
	// DataClass is "private" (default) or "public". Only public tasks may use
	// model routes that may log prompts (e.g. free endpoints).
	DataClass string `json:"data_class"`
}

// IsPublic reports whether the task's content may go to prompt-logging routes.
func (s Scope) IsPublic() bool { return s.DataClass == "public" }

type Task struct {
	ID              string  `json:"id"`
	Objective       string  `json:"objective"`
	State           State   `json:"state"`
	Reason          *string `json:"reason"`
	WaitReason      *string `json:"wait_reason"`
	ParentTaskID    *string `json:"parent_task_id"`
	RootTaskID      string  `json:"root_task_id"`
	AgentRole       string  `json:"agent_role"`
	Depth           int     `json:"depth"`
	Budget          Budget  `json:"budget"`
	Scope           Scope   `json:"scope"`
	ConfigHash      string  `json:"-"`
	IdempotencyKey  *string `json:"-"`
	StartedAt       *int64  `json:"started_at"`
	CostReservedUSD float64 `json:"cost_reserved_usd"`
	CostUSD         float64 `json:"cost_usd"`
	CreatedAt       int64   `json:"created_at"`
	UpdatedAt       int64   `json:"updated_at"`
}

type Event struct {
	Seq        int64           `json:"seq"`
	Type       string          `json:"type"`
	StepID     *string         `json:"step_id"`
	Actor      string          `json:"actor"`
	Visibility string          `json:"visibility"`
	Data       json.RawMessage `json:"data"`
	At         int64           `json:"at"`
}

// Insert creates a task in CREATED state with its TASK_CREATED event.
func Insert(ctx context.Context, tx *sql.Tx, t *Task) error {
	now := ids.NowMs()
	t.ID = ids.New()
	if t.RootTaskID == "" {
		t.RootTaskID = t.ID
	}
	if t.AgentRole == "" {
		t.AgentRole = "main"
	}
	t.State = Created
	t.CreatedAt, t.UpdatedAt = now, now
	b, _ := json.Marshal(t.Budget)
	sc, _ := json.Marshal(t.Scope)
	_, err := tx.ExecContext(ctx, `insert into tasks(id, objective, state, parent_task_id, root_task_id, agent_role, depth,
		budget_json, scope_json, config_hash, idempotency_key, created_at, updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Objective, t.State, t.ParentTaskID, t.RootTaskID, t.AgentRole, t.Depth, string(b), string(sc),
		t.ConfigHash, t.IdempotencyKey, now, now)
	if err != nil {
		return err
	}
	_, err = AppendEvent(ctx, tx, t.ID, nil, "TASK_CREATED", "owner", map[string]any{
		"budget": t.Budget, "tools": t.Scope.Tools, "allow_domains": t.Scope.AllowDomains, "data_class": t.Scope.DataClass,
		"config_hash": t.ConfigHash, "objective_chars": len(t.Objective)})
	return err
}

// AppendEvent appends an event with the next per-task sequence number. Data is
// redacted before it is stored (ADR-0008).
func AppendEvent(ctx context.Context, tx *sql.Tx, taskID string, stepID *string, typ, actor string, data any) (int64, error) {
	return appendEvent(ctx, tx, taskID, stepID, typ, actor, "timeline", data)
}

// AppendInternalEvent appends an event hidden from the activity timeline.
func AppendInternalEvent(ctx context.Context, tx *sql.Tx, taskID string, stepID *string, typ, actor string, data any) (int64, error) {
	return appendEvent(ctx, tx, taskID, stepID, typ, actor, "internal", data)
}

func appendEvent(ctx context.Context, tx *sql.Tx, taskID string, stepID *string, typ, actor, vis string, data any) (int64, error) {
	if data == nil {
		data = map[string]any{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, "update tasks set last_seq = last_seq + 1 where id = ? returning last_seq", taskID).Scan(&seq); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `insert into task_events(id, task_id, seq, step_id, type, actor, visibility, data_json, at)
		values(?,?,?,?,?,?,?,?,?)`, ids.New(), taskID, seq, stepID, typ, actor, vis, obs.Redact(string(b)), ids.NowMs())
	return seq, err
}

// Transition moves a task to a new state if legal, records reason/wait reason
// and appends STATE_CHANGED. It returns the previous state.
func Transition(ctx context.Context, tx *sql.Tx, id string, to State, reason, waitReason string, actor string) (State, error) {
	var from State
	if err := tx.QueryRowContext(ctx, "select state from tasks where id = ?", id).Scan(&from); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if !CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	now := ids.NowMs()
	_, err := tx.ExecContext(ctx, `update tasks set state=?, reason=?, wait_reason=?, version=version+1, updated_at=?,
		started_at=coalesce(started_at, case when ?='PLANNING' then ? end) where id=?`,
		to, nullable(reason), nullable(waitReason), now, string(to), now, id)
	if err != nil {
		return from, err
	}
	_, err = AppendEvent(ctx, tx, id, nil, "STATE_CHANGED", actor, map[string]any{
		"from": from, "to": to, "reason": reason, "wait_reason": waitReason})
	return from, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const taskCols = `id, objective, state, reason, wait_reason, parent_task_id, root_task_id, agent_role, depth,
	budget_json, scope_json, config_hash, idempotency_key, started_at, cost_reserved_usd, cost_actual_usd, created_at, updated_at`

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var b, s string
	err := sc.Scan(&t.ID, &t.Objective, &t.State, &t.Reason, &t.WaitReason, &t.ParentTaskID, &t.RootTaskID,
		&t.AgentRole, &t.Depth, &b, &s, &t.ConfigHash, &t.IdempotencyKey, &t.StartedAt, &t.CostReservedUSD,
		&t.CostUSD, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(b), &t.Budget)
	json.Unmarshal([]byte(s), &t.Scope)
	return &t, nil
}

// Get loads one task.
func Get(ctx context.Context, q store.Querier, id string) (*Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, "select "+taskCols+" from tasks where id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// GetByIdempotencyKey finds a task created with the same Idempotency-Key.
func GetByIdempotencyKey(ctx context.Context, q store.Querier, key string) (*Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, "select "+taskCols+" from tasks where idempotency_key = ?", key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// List returns tasks newest first; cursor is the last seen task ID.
func List(ctx context.Context, q store.Querier, state State, limit int, cursor string) ([]*Task, error) {
	var where []string
	var args []any
	if state != "" {
		where, args = append(where, "state = ?"), append(args, state)
	}
	if cursor != "" {
		where, args = append(where, "id < ?"), append(args, cursor)
	}
	query := "select " + taskCols + " from tasks"
	if len(where) > 0 {
		query += " where " + strings.Join(where, " and ")
	}
	query += " order by id desc limit ?"
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Runnable returns the oldest task the engine should drive, if any.
func Runnable(ctx context.Context, q store.Querier) (*Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, "select "+taskCols+
		" from tasks where state in ('CREATED','PLANNING','RUNNING') order by created_at, id limit 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// Events returns a task's events after a sequence number.
func Events(ctx context.Context, q store.Querier, taskID string, afterSeq int64, includeInternal bool) ([]Event, error) {
	query := "select seq, type, step_id, actor, visibility, data_json, at from task_events where task_id = ? and seq > ?"
	if !includeInternal {
		query += " and visibility = 'timeline'"
	}
	rows, err := q.QueryContext(ctx, query+" order by seq", taskID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var d string
		if err := rows.Scan(&e.Seq, &e.Type, &e.StepID, &e.Actor, &e.Visibility, &d, &e.At); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(d)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SetLease records which Core run currently drives the task (ADR-0013).
func SetLease(ctx context.Context, tx *sql.Tx, id, owner string, expiresMs int64) error {
	_, err := tx.ExecContext(ctx, "update tasks set lease_owner=?, lease_expires_at=? where id=?", owner, expiresMs, id)
	return err
}
