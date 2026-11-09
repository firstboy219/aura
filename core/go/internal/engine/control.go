package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

// ErrInvalid marks a request the API should answer with 400.
var ErrInvalid = errors.New("invalid_input")

// CreateInput is a validated task request.
type CreateInput struct {
	Objective      string
	Tools          []string
	AllowDomains   []string
	Budget         task.Budget
	IdempotencyKey string
	DataClass      string // "private" (default) or "public"
}

// Submit validates and stores a new task. A repeated Idempotency-Key returns
// the original task instead of creating a duplicate.
func (e *Engine) Submit(ctx context.Context, in CreateInput) (*task.Task, bool, error) {
	if in.IdempotencyKey != "" {
		if t, err := task.GetByIdempotencyKey(ctx, e.st.R, in.IdempotencyKey); err == nil {
			return t, false, nil
		}
	}
	t, err := e.validate(in)
	if err != nil {
		return nil, false, err
	}
	err = e.st.Write(ctx, func(tx *sql.Tx) error { return task.Insert(ctx, tx, t) })
	if err != nil {
		if in.IdempotencyKey != "" && strings.Contains(err.Error(), "UNIQUE") {
			t2, gerr := task.GetByIdempotencyKey(ctx, e.st.R, in.IdempotencyKey)
			return t2, false, gerr
		}
		return nil, false, err
	}
	e.Wake()
	return t, true, nil
}

func (e *Engine) validate(in CreateInput) (*task.Task, error) {
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, a...)) }
	obj := strings.TrimSpace(in.Objective)
	if obj == "" || len(obj) > 8000 {
		return nil, bad("objective must be 1..8000 characters")
	}
	if len(in.Tools) == 0 {
		in.Tools = []string{"web.fetch", "workspace.read", "workspace.write"}
	}
	seen := map[string]bool{}
	var tools []string
	for _, tl := range in.Tools {
		if _, ok := policy.Manifest[tl]; !ok {
			return nil, bad("unknown tool %q", tl)
		}
		if !seen[tl] {
			seen[tl] = true
			tools = append(tools, tl)
		}
	}
	if len(in.AllowDomains) > 50 {
		return nil, bad("at most 50 allow_domains")
	}
	var domains []string
	for _, d := range append(append([]string{}, e.cfg.Tools.WebFetch.AllowDomains...), in.AllowDomains...) {
		d = strings.ToLower(strings.TrimSpace(d))
		if !validDomain(d) {
			return nil, bad("invalid domain %q", d)
		}
		domains = append(domains, d)
	}
	def := e.cfg.Budgets.TaskDefault
	b := task.Budget{MaxCostUSD: def.MaxCostUSD, MaxIterations: def.MaxIterations, MaxWallSeconds: def.MaxWallSeconds}
	if in.Budget.MaxCostUSD > 0 {
		b.MaxCostUSD = in.Budget.MaxCostUSD
	}
	if in.Budget.MaxIterations > 0 {
		b.MaxIterations = in.Budget.MaxIterations
	}
	if in.Budget.MaxWallSeconds > 0 {
		b.MaxWallSeconds = in.Budget.MaxWallSeconds
	}
	if b.MaxCostUSD > def.MaxCostUSD || b.MaxIterations > def.MaxIterations || b.MaxWallSeconds > def.MaxWallSeconds {
		return nil, bad("budget exceeds configured task_default caps")
	}
	dc := in.DataClass
	switch dc {
	case "":
		dc = "private"
	case "private", "public":
	default:
		return nil, bad("data_class must be private or public")
	}
	t := &task.Task{Objective: obj, Budget: b, Scope: task.Scope{Tools: tools, AllowDomains: domains, DataClass: dc}, ConfigHash: e.cfg.Hash()}
	if in.IdempotencyKey != "" {
		if len(in.IdempotencyKey) > 128 {
			return nil, bad("Idempotency-Key too long")
		}
		k := in.IdempotencyKey
		t.IdempotencyKey = &k
	}
	return t, nil
}

// validDomain accepts host names and "*.host" wildcards, never IP literals.
func validDomain(d string) bool {
	h := strings.TrimPrefix(d, "*.")
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func (e *Engine) interrupt(id, why string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current == id && e.cancel != nil {
		e.why = why
		e.cancel()
	}
}

func (e *Engine) owner(ctx context.Context, id string, fn func(tx *sql.Tx, t *task.Task) error) (*task.Task, error) {
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		t, err := task.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		return fn(tx, t)
	})
	if err != nil {
		return nil, err
	}
	return task.Get(ctx, e.st.R, id)
}

// Cancel moves a task to CANCELLED, revokes its grants, invalidates pending
// approvals and interrupts it if it is running.
func (e *Engine) Cancel(ctx context.Context, id string) (*task.Task, error) {
	t, err := e.owner(ctx, id, func(tx *sql.Tx, t *task.Task) error {
		if _, err := task.Transition(ctx, tx, id, task.Cancelled, "cancelled_by_owner", "", "owner"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update approvals set status='invalidated' where task_id=? and status in ('pending','approved')", id); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, id)
	})
	if err == nil {
		e.interrupt(id, "cancel")
	}
	return t, err
}

// Pause moves a task to PAUSED and interrupts it if it is running.
func (e *Engine) Pause(ctx context.Context, id string) (*task.Task, error) {
	t, err := e.owner(ctx, id, func(tx *sql.Tx, t *task.Task) error {
		_, err := task.Transition(ctx, tx, id, task.Paused, "paused_by_owner", "owner", "owner")
		return err
	})
	if err == nil {
		e.interrupt(id, "pause")
	}
	return t, err
}

// Resume returns a paused task to where it was: waiting for an approval,
// running its plan, or planning from scratch if no plan was stored.
func (e *Engine) Resume(ctx context.Context, id string) (*task.Task, error) {
	if e.gov.Halted() {
		return nil, fmt.Errorf("%w: kill switch active; restart the Core to clear it", task.ErrInvalidTransition)
	}
	t, err := e.owner(ctx, id, func(tx *sql.Tx, t *task.Task) error {
		if t.State != task.Paused {
			return fmt.Errorf("%w: task is %s", task.ErrInvalidTransition, t.State)
		}
		var steps, waiting int
		tx.QueryRowContext(ctx, "select count(*), coalesce(sum(state='WAITING_APPROVAL'),0) from task_steps where task_id=?", id).Scan(&steps, &waiting)
		var pending int
		tx.QueryRowContext(ctx, "select count(*) from approvals where task_id=? and status='pending'", id).Scan(&pending)
		to := task.Created
		switch {
		case waiting > 0 && pending > 0:
			to = task.WaitingApproval
		case steps > 0:
			to = task.Running
		}
		_, err := task.Transition(ctx, tx, id, to, "resumed_by_owner", "", "owner")
		return err
	})
	if err == nil {
		e.Wake()
	}
	return t, err
}

// Approve records the owner's approval of the exact action hash and resumes the task.
func (e *Engine) Approve(ctx context.Context, approvalID, hash, by string) (*policy.Approval, error) {
	var ap *policy.Approval
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		ap, err = policy.Decide(ctx, tx, approvalID, true, hash, by)
		if err != nil {
			return err
		}
		if err := policy.Audit(ctx, tx, "APPROVAL_GRANTED", "owner", ap.TaskID, "", map[string]any{"approval_id": ap.ID, "action_hash": ap.ActionHash}); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, ap.TaskID, &ap.StepID, "APPROVAL_DECIDED", "owner", map[string]any{"approval_id": ap.ID, "approved": true}); err != nil {
			return err
		}
		cur, err := task.Get(ctx, tx, ap.TaskID)
		if err != nil {
			return err
		}
		if cur.State == task.WaitingApproval {
			_, err = task.Transition(ctx, tx, ap.TaskID, task.Running, "approved", "", "owner")
		}
		return err
	})
	if errors.Is(err, policy.ErrExpired) && ap != nil {
		e.failForApproval(ctx, ap, "approval_expired")
	}
	if err == nil {
		e.Wake()
	}
	return ap, err
}

// Reject records the owner's rejection; the step and task fail.
func (e *Engine) Reject(ctx context.Context, approvalID, by string) (*policy.Approval, error) {
	var ap *policy.Approval
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		ap, err = policy.Decide(ctx, tx, approvalID, false, "", by)
		if err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "APPROVAL_REJECTED", "owner", ap.TaskID, "", map[string]any{"approval_id": ap.ID})
	})
	if err == nil || errors.Is(err, policy.ErrExpired) {
		if ap != nil {
			reason := "approval_rejected"
			if err != nil {
				reason = "approval_expired"
			}
			e.failForApproval(ctx, ap, reason)
		}
	}
	return ap, err
}

func (e *Engine) failForApproval(ctx context.Context, ap *policy.Approval, reason string) {
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		st, err := task.GetStep(ctx, tx, ap.StepID)
		if err != nil {
			return err
		}
		if err := task.UpdateStep(ctx, tx, st, task.StepFailed, "", "", reason, "STEP_FAILED", map[string]any{"approval_id": ap.ID}); err != nil {
			return err
		}
		cur, err := task.Get(ctx, tx, ap.TaskID)
		if err != nil || cur.State.Terminal() {
			return err
		}
		if _, err := task.Transition(ctx, tx, ap.TaskID, task.Failed, reason, "", "core"); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, ap.TaskID)
	})
}

// ExpireApprovals fails tasks whose pending approval passed its expiry.
func (e *Engine) ExpireApprovals(ctx context.Context) {
	rows, err := e.st.R.QueryContext(ctx, "select id from approvals where status='pending' and expires_at<=?", ids.NowMs())
	if err != nil {
		return
	}
	var expired []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		expired = append(expired, id)
	}
	rows.Close()
	for _, id := range expired {
		var ap *policy.Approval
		e.st.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "update approvals set status='expired' where id=? and status='pending'", id); err != nil {
				return err
			}
			var err error
			ap, err = policy.GetApproval(ctx, tx, id)
			return err
		})
		if ap != nil {
			e.failForApproval(ctx, ap, "approval_expired")
		}
	}
}

// Stop is the kill switch (Blueprint §8.4): block new provider calls, cancel
// the in-flight task (which cancels its provider request), kill the worker,
// revoke every grant and pause active tasks so their state stays recoverable.
func (e *Engine) Stop(ctx context.Context, by string) (int, error) {
	e.gov.Halt()
	e.mu.Lock()
	if e.cancel != nil {
		e.why = "kill_switch"
		e.cancel()
	}
	e.mu.Unlock()
	e.sup.Kill()
	n := 0
	err := e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "select id from tasks where state in ('CREATED','PLANNING','RUNNING')")
		if err != nil {
			return err
		}
		var active []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			active = append(active, id)
		}
		rows.Close()
		for _, id := range active {
			if _, err := task.Transition(ctx, tx, id, task.Paused, "kill_switch", "kill_switch", "owner"); err != nil {
				return err
			}
			n++
		}
		if err := policy.RevokeAll(ctx, tx); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "KILL_SWITCH", by, "", "", map[string]any{"paused_tasks": n})
	})
	return n, err
}

// Store exposes the store for read-side API handlers.
func (e *Engine) Store() *store.Store { return e.st }
