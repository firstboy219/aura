package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
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
	Tier           string // optional owner override: fast | standard | deep
	Recovery       string // auto (default) | manual | never (Blueprint §8.2)
	BrowserProfile string // owner login profile for browser tasks (ADR-0040)
	// Attachments are owner-provided input files (workspace path → text content).
	Attachments map[string]string
	// Reply is "none" to skip the owner reply (ADR-0055); empty means the default.
	Reply string
	// CreatedBy is the principal that asked (member id); recorded with the task.
	CreatedBy string
	// Voice carries the recognizer's hypotheses when the objective was spoken (ADR-0058).
	Voice *task.Voice
	// Design names the app screen to redesign (ADR-0060).
	Design string
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
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := task.Insert(ctx, tx, t); err != nil {
			return err
		}
		if in.CreatedBy != "" {
			if _, err := tx.ExecContext(ctx, "update tasks set created_by=? where id=?", in.CreatedBy, t.ID); err != nil {
				return err
			}
		}
		if len(in.Attachments) == 0 {
			return nil
		}
		// Files are written before commit: if the commit fails they are orphans in
		// an unused directory, never files of a different task.
		var files []map[string]any
		for _, p := range t.Scope.Attachments {
			res, err := e.ws.Write(t.ID, p, in.Attachments[p])
			if err != nil {
				return fmt.Errorf("%w: attachment %s: %v", ErrInvalid, p, err)
			}
			files = append(files, map[string]any{"path": p, "sha256": res.SHA256, "bytes": res.Bytes})
		}
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, "ATTACHMENTS_ADDED", "owner", map[string]any{"files": files})
		return err
	})
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
		in.Tools = append([]string{"web.fetch", "web.search", "news.headlines", "workspace.read", "workspace.write", "data.read", "calc", tools.JarvisTool},
			tools.InfoTools...)
		if e.shell != nil {
			in.Tools = append(in.Tools, "shell.run") // server commands, under the watcher (ADR-0065)
		}
	}
	seen := map[string]bool{}
	var granted []string
	for _, tl := range in.Tools {
		if _, ok := policy.Lookup(tl); !ok {
			return nil, bad("unknown tool %q", tl)
		}
		if !seen[tl] {
			seen[tl] = true
			granted = append(granted, tl)
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
	switch in.Recovery {
	case "", "auto", "manual", "never":
	default:
		return nil, bad("recovery must be auto, manual or never")
	}
	if in.Reply != "" && in.Reply != "none" {
		return nil, bad("reply must be empty or none")
	}
	rec := in.Recovery
	if rec == "auto" {
		rec = ""
	}
	if in.BrowserProfile != "" {
		if _, ok := e.loginProfile(in.BrowserProfile); !ok {
			return nil, bad("browser_profile is not a configured login profile")
		}
	}
	t := &task.Task{Objective: obj, Budget: b, Scope: task.Scope{Tools: granted, AllowDomains: domains, DataClass: dc, Recovery: rec,
		BrowserProfile: in.BrowserProfile, Reply: in.Reply}, ConfigHash: e.cfg.Hash()}
	if in.Design != "" {
		if !knownScreen(in.Design) {
			return nil, bad("unknown screen %q", in.Design)
		}
		t.Scope.Design = in.Design
	}
	if in.Voice != nil {
		v, err := cleanVoice(in.Voice)
		if err != nil {
			return nil, bad("%v", err)
		}
		t.Scope.Voice = v
	}
	switch in.Tier {
	case "":
	case "fast", "standard", "deep":
		tier := in.Tier
		t.TierOverride = &tier
	default:
		return nil, bad("tier must be fast, standard or deep")
	}
	if len(in.Attachments) > 10 {
		return nil, bad("at most 10 attachments")
	}
	for p, c := range in.Attachments {
		cp, err := tools.CleanPath(p)
		if err != nil || cp != p || strings.HasPrefix(p, policy.ProtectedPrefix) {
			return nil, bad("invalid attachment path %q", p)
		}
		if len(c) > 256<<10 {
			return nil, bad("attachment %q is larger than 256 KiB", p)
		}
		t.Scope.Attachments = append(t.Scope.Attachments, p)
	}
	sort.Strings(t.Scope.Attachments)
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
	if d, ok := e.driving[id]; ok && d.cancel != nil {
		d.why = why
		d.cancel()
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
	var cancelled []string
	t, err := e.owner(ctx, id, func(tx *sql.Tx, t *task.Task) error {
		if _, err := task.Transition(ctx, tx, id, task.Cancelled, "cancelled_by_owner", "", "owner"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update approvals set status='invalidated' where task_id=? and status in ('pending','approved')", id); err != nil {
			return err
		}
		ds, err := cancelDescendants(ctx, tx, id)
		if err != nil {
			return err
		}
		cancelled = ds
		return policy.RevokeTask(ctx, tx, id)
	})
	if err == nil {
		e.interrupt(id, "cancel")
		for _, d := range cancelled {
			e.interrupt(d, "cancel")
		}
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
		var pending, inputs, waits int
		tx.QueryRowContext(ctx, "select count(*) from approvals where task_id=? and status='pending'", id).Scan(&pending)
		tx.QueryRowContext(ctx, "select count(*) from task_inputs where task_id=? and status='pending'", id).Scan(&inputs)
		tx.QueryRowContext(ctx, "select count(*) from task_waits where task_id=? and satisfied_at is null", id).Scan(&waits)
		to := task.Created
		switch {
		case inputs > 0:
			to = task.WaitingInput
		case waits > 0:
			to = task.Waiting
			var timeWaits int
			tx.QueryRowContext(ctx, "select count(*) from task_waits where task_id=? and satisfied_at is null and kind='time'", id).Scan(&timeWaits)
			if timeWaits == waits {
				to = task.Scheduled
			}
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

// Answer records the owner's answer to the task's pending question and resumes
// planning (or the step that asked).
func (e *Engine) Answer(ctx context.Context, taskID, answer string) (*task.Task, error) {
	answer = strings.TrimSpace(answer)
	if answer == "" || len(answer) > 4000 {
		return nil, fmt.Errorf("%w: answer must be 1..4000 characters", ErrInvalid)
	}
	t, err := e.owner(ctx, taskID, func(tx *sql.Tx, t *task.Task) error {
		if t.State != task.WaitingInput {
			return fmt.Errorf("%w: task is %s, not waiting for input", task.ErrInvalidTransition, t.State)
		}
		var id string
		var stepID sql.NullString
		if err := tx.QueryRowContext(ctx, "select id, step_id from task_inputs where task_id=? and status='pending' order by created_at limit 1",
			taskID).Scan(&id, &stepID); err != nil {
			return fmt.Errorf("%w: no pending question", task.ErrInvalidTransition)
		}
		if _, err := tx.ExecContext(ctx, "update task_inputs set status='answered', answer=?, answered_at=? where id=?", answer, ids.NowMs(), id); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, taskID, nil, "INPUT_ANSWERED", "owner", map[string]any{"input_id": id, "answer_chars": len(answer)}); err != nil {
			return err
		}
		var steps int
		tx.QueryRowContext(ctx, "select count(*) from task_steps where task_id=?", taskID).Scan(&steps)
		to := task.Planning
		if steps > 0 {
			to = task.Running
		}
		_, err := task.Transition(ctx, tx, taskID, to, "answered", "", "owner")
		return err
	})
	if err == nil {
		e.Wake()
	}
	return t, err
}

// PendingQuestion returns the task's open clarification question, if any.
func (e *Engine) PendingQuestion(ctx context.Context, taskID string) *string {
	var q string
	if err := e.st.R.QueryRowContext(ctx, "select question from task_inputs where task_id=? and status='pending' order by created_at limit 1", taskID).Scan(&q); err != nil {
		return nil
	}
	return &q
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
		if err := policy.Audit(ctx, tx, "APPROVAL_GRANTED", "owner", ap.TaskID, "", map[string]any{"approval_id": ap.ID, "action_hash": ap.ActionHash, "risk_class": ap.RiskClass, "auth_strength": ap.AuthStrength, "policy_version": ap.PolicyVersion}); err != nil {
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

// RevokeApproval is the owner withdrawing a pending or approved-but-unused approval: the approval is invalidated, the step and task fail, and the
// task's grants are revoked (ODI A3, ADR-0072).
func (e *Engine) RevokeApproval(ctx context.Context, approvalID, by, reason string) (*policy.Approval, error) {
	var ap *policy.Approval
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		ap, err = policy.RevokeApproval(ctx, tx, approvalID, by, reason)
		if err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "APPROVAL_REVOKED", "owner", ap.TaskID, "", map[string]any{"approval_id": ap.ID, "action_hash": ap.ActionHash, "by": by, "reason": reason})
	})
	if err == nil {
		e.failForApproval(ctx, ap, "approval_revoked")
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
	for _, d := range e.driving {
		if d.cancel != nil {
			d.why = "kill_switch"
			d.cancel()
		}
	}
	e.mu.Unlock()
	for _, s := range e.sups {
		s.Kill()
	}
	e.CloseAllBrowserSessions()
	e.KillShell(context.WithoutCancel(ctx))
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

// cleanVoice bounds a spoken turn's hypotheses: 1..5 alternatives, each 1..2000
// characters, confidence within 0..1. Duplicates are dropped.
func cleanVoice(v *task.Voice) (*task.Voice, error) {
	if len(v.Alternatives) == 0 || len(v.Alternatives) > 5 {
		return nil, fmt.Errorf("voice needs 1..5 alternatives")
	}
	out := &task.Voice{}
	seen := map[string]bool{}
	for _, a := range v.Alternatives {
		a.Text = strings.TrimSpace(a.Text)
		if a.Text == "" || len(a.Text) > 2000 {
			return nil, fmt.Errorf("voice alternatives must be 1..2000 characters")
		}
		if a.Confidence != nil && (*a.Confidence < 0 || *a.Confidence > 1) {
			return nil, fmt.Errorf("voice confidence must be within 0..1")
		}
		if k := strings.ToLower(a.Text); !seen[k] {
			seen[k] = true
			out.Alternatives = append(out.Alternatives, a)
		}
	}
	return out, nil
}
