package engine

// Scheduled triggers and Proactive Intelligence v1 (ADR-0028). Triggers create
// ordinary tasks (normal validation, budgets, grants). Watches are read-only:
// the Core fetches each source itself and compares a content hash; only a
// change costs a model call, made in a session with no tools at all, so a
// watch can never write, send or browse. Findings become owner notifications.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

// TriggerSpec is the stored definition of a trigger.
type TriggerSpec struct {
	Tools        []string    `json:"tools,omitempty"`
	AllowDomains []string    `json:"allow_domains,omitempty"`
	DataClass    string      `json:"data_class,omitempty"`
	Budget       task.Budget `json:"budget"`
	Sources      []string    `json:"sources,omitempty"` // watch only
	Purpose      string      `json:"purpose,omitempty"` // watch only
}

// Trigger is one schedule.
type Trigger struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Kind            string      `json:"kind"`
	IntervalSeconds *int64      `json:"interval_seconds"`
	DailyAt         *string     `json:"daily_at"`
	Objective       string      `json:"objective"`
	Spec            TriggerSpec `json:"spec"`
	Enabled         bool        `json:"enabled"`
	NextRunAt       int64       `json:"next_run_at"`
	LastRunAt       *int64      `json:"last_run_at"`
	LastTaskID      *string     `json:"last_task_id"`
}

const trigCols = "id, name, kind, interval_seconds, daily_at, objective, spec_json, enabled, next_run_at, last_run_at, last_task_id"

func scanTrigger(sc interface{ Scan(...any) error }) (*Trigger, error) {
	var t Trigger
	var spec string
	var enabled int
	if err := sc.Scan(&t.ID, &t.Name, &t.Kind, &t.IntervalSeconds, &t.DailyAt, &t.Objective, &spec, &enabled, &t.NextRunAt, &t.LastRunAt, &t.LastTaskID); err != nil {
		return nil, err
	}
	t.Enabled = enabled == 1
	json.Unmarshal([]byte(spec), &t.Spec)
	return &t, nil
}

// nextRun computes the next run time after now. Missed runs collapse into one.
func nextRun(interval *int64, dailyAt *string, now time.Time, def *time.Location) (time.Time, error) {
	if interval != nil {
		return now.Add(time.Duration(*interval) * time.Second), nil
	}
	if dailyAt == nil {
		return time.Time{}, errors.New("no schedule")
	}
	// "07:30" uses the workspace time zone; "07:30+07:00" names its own offset.
	at, err := time.ParseInLocation("15:04", *dailyAt, def)
	if err != nil {
		at, err = time.Parse("15:04Z07:00", *dailyAt)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("daily_at must look like 07:30 (workspace time zone) or 07:30+07:00: %v", err)
	}
	loc := at.Location()
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), at.Hour(), at.Minute(), 0, 0, loc)
	if !next.After(n) {
		next = next.Add(24 * time.Hour)
	}
	return next, nil
}

// CreateTrigger validates and stores a trigger. Watch sources must be public
// http(s) URLs inside the allowlist.
func (e *Engine) CreateTrigger(ctx context.Context, tr *Trigger) (*Trigger, error) {
	bad := func(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, a...)) }
	tr.Name = strings.TrimSpace(tr.Name)
	if tr.Name == "" || len(tr.Name) > 80 {
		return nil, bad("name must be 1..80 characters")
	}
	if (tr.IntervalSeconds == nil) == (tr.DailyAt == nil) {
		return nil, bad("set exactly one of interval_seconds or daily_at")
	}
	if tr.IntervalSeconds != nil && *tr.IntervalSeconds < 60 {
		return nil, bad("interval_seconds must be at least 60")
	}
	next, err := nextRun(tr.IntervalSeconds, tr.DailyAt, time.Now(), e.cfg.Location())
	if err != nil {
		return nil, bad("%v", err)
	}
	switch tr.Kind {
	case "task":
		// Validate exactly as a task would be (tools, domains, budget caps).
		if _, err := e.validate(CreateInput{Objective: tr.Objective, Tools: tr.Spec.Tools, AllowDomains: tr.Spec.AllowDomains,
			Budget: tr.Spec.Budget, DataClass: tr.Spec.DataClass}); err != nil {
			return nil, err
		}
	case "watch":
		if len(tr.Spec.Sources) == 0 || len(tr.Spec.Sources) > 20 {
			return nil, bad("a watch needs 1..20 sources")
		}
		for _, s := range tr.Spec.Sources {
			u, err := url.Parse(s)
			if err != nil || tools.CheckURL(u, tr.Spec.AllowDomains) != nil {
				return nil, bad("source %q must be an http(s) URL in allow_domains", s)
			}
		}
		tr.Spec.Tools, tr.Spec.DataClass = nil, "public" // read-only by construction; sources are public
		if tr.Objective == "" {
			tr.Objective = "Watch: " + tr.Name
		}
		def := e.cfg.Budgets.TaskDefault
		if tr.Spec.Budget.MaxCostUSD <= 0 || tr.Spec.Budget.MaxCostUSD > def.MaxCostUSD {
			tr.Spec.Budget.MaxCostUSD = def.MaxCostUSD
		}
		tr.Spec.Budget.MaxIterations, tr.Spec.Budget.MaxWallSeconds = 4, 300
	default:
		return nil, bad("kind must be task or watch")
	}
	spec, _ := json.Marshal(tr.Spec)
	tr.ID, tr.Enabled, tr.NextRunAt = ids.New(), true, next.UnixMilli()
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		now := ids.NowMs()
		if _, err := tx.ExecContext(ctx, `insert into triggers(id, name, kind, interval_seconds, daily_at, objective, spec_json, enabled,
			next_run_at, created_at, updated_at) values(?,?,?,?,?,?,?,1,?,?,?)`, tr.ID, tr.Name, tr.Kind, tr.IntervalSeconds, tr.DailyAt,
			tr.Objective, string(spec), tr.NextRunAt, now, now); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return bad("a trigger named %q exists", tr.Name)
			}
			return err
		}
		return policy.Audit(ctx, tx, "TRIGGER_CREATED", "owner", "", "", map[string]any{"id": tr.ID, "kind": tr.Kind})
	})
	if err != nil {
		return nil, err
	}
	return tr, nil
}

// ListTriggers returns all triggers.
func (e *Engine) ListTriggers(ctx context.Context) ([]*Trigger, error) {
	rows, err := e.st.R.QueryContext(ctx, "select "+trigCols+" from triggers order by name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Trigger{}
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TriggerAction enables, disables, deletes, or runs a trigger now.
func (e *Engine) TriggerAction(ctx context.Context, id, action string) (*Trigger, error) {
	var t *Trigger
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if t, err = scanTrigger(tx.QueryRowContext(ctx, "select "+trigCols+" from triggers where id=?", id)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return task.ErrNotFound
			}
			return err
		}
		switch action {
		case "enable", "disable":
			v := map[string]int{"enable": 1, "disable": 0}[action]
			_, err = tx.ExecContext(ctx, "update triggers set enabled=?, updated_at=? where id=?", v, ids.NowMs(), id)
			t.Enabled = v == 1
		case "run":
			_, err = tx.ExecContext(ctx, "update triggers set next_run_at=?, updated_at=? where id=?", ids.NowMs(), ids.NowMs(), id)
		case "delete":
			if _, err = tx.ExecContext(ctx, "delete from watch_state where trigger_id=?", id); err == nil {
				if _, err = tx.ExecContext(ctx, "update notifications set trigger_id=null where trigger_id=?", id); err == nil {
					_, err = tx.ExecContext(ctx, "delete from triggers where id=?", id)
				}
			}
		default:
			return fmt.Errorf("%w: action must be enable, disable, run or delete", ErrInvalid)
		}
		if err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "TRIGGER_"+strings.ToUpper(action), "owner", "", "", map[string]any{"id": id})
	})
	if err == nil && action == "run" {
		e.Wake()
	}
	return t, err
}

// RunTriggers fires due triggers. Called from the scheduler tick (SQL only
// unless something is due).
func (e *Engine) RunTriggers(ctx context.Context) {
	rows, err := e.st.R.QueryContext(ctx, "select "+trigCols+" from triggers where enabled=1 and next_run_at<=?", ids.NowMs())
	if err != nil {
		return
	}
	var due []*Trigger
	for rows.Next() {
		if t, err := scanTrigger(rows); err == nil {
			due = append(due, t)
		}
	}
	rows.Close()
	for _, tr := range due {
		slot := tr.NextRunAt
		next, _ := nextRun(tr.IntervalSeconds, tr.DailyAt, time.Now(), e.cfg.Location())
		// Claim the slot first: a crash after this never runs the same slot twice.
		claimed := false
		e.st.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, "update triggers set next_run_at=?, last_run_at=? where id=? and next_run_at=?", next.UnixMilli(), ids.NowMs(), tr.ID, slot)
			if err == nil {
				n, _ := res.RowsAffected()
				claimed = n == 1
			}
			return err
		})
		if !claimed {
			continue
		}
		var taskID string
		var err error
		if tr.Kind == "task" {
			var t *task.Task
			t, _, err = e.Submit(ctx, CreateInput{Objective: tr.Objective, Tools: tr.Spec.Tools, AllowDomains: tr.Spec.AllowDomains,
				Budget: tr.Spec.Budget, DataClass: tr.Spec.DataClass, IdempotencyKey: fmt.Sprintf("trigger:%s:%d", tr.ID, slot)})
			if t != nil {
				taskID = t.ID
			}
		} else {
			taskID, err = e.runWatch(ctx, tr)
		}
		if err != nil {
			e.log.Warn("trigger run failed", "trigger", tr.Name, "err", err)
		}
		if taskID != "" {
			e.st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "update triggers set last_task_id=? where id=?", taskID, tr.ID)
				return err
			})
		}
	}
}

type watchChange struct {
	Source   string `json:"source"`
	Previous string `json:"previous"`
	Current  string `json:"current"`
}

// runWatch fetches every source; unchanged sources cost nothing. Changed ones
// are summarized by the worker in a session with no tools (read-only).
func (e *Engine) runWatch(ctx context.Context, tr *Trigger) (string, error) {
	var changes []watchChange
	for _, src := range tr.Spec.Sources {
		res, err := e.fetch.Fetch(ctx, src, tr.Spec.AllowDomains)
		if err != nil || res.Status >= 400 {
			continue
		}
		text := truncate(res.Text, 20000)
		var prevHash, prev string
		e.st.R.QueryRowContext(ctx, "select content_sha256, content from watch_state where trigger_id=? and source=?", tr.ID, src).Scan(&prevHash, &prev)
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `insert into watch_state(trigger_id, source, content_sha256, content, checked_at) values(?,?,?,?,?)
				on conflict(trigger_id, source) do update set content_sha256=excluded.content_sha256, content=excluded.content, checked_at=excluded.checked_at`,
				tr.ID, src, res.SHA256, text, ids.NowMs())
			return err
		})
		if prevHash != "" && prevHash != res.SHA256 {
			changes = append(changes, watchChange{Source: src, Previous: truncate(prev, 6000), Current: truncate(text, 6000)})
		}
	}
	if len(changes) == 0 {
		return "", nil // baseline or unchanged: no model call
	}
	t := &task.Task{Objective: tr.Objective + " — summarize changes in watched sources", AgentRole: "watch",
		Budget: tr.Spec.Budget, Scope: task.Scope{Tools: []string{}, AllowDomains: tr.Spec.AllowDomains, DataClass: "public"},
		ConfigHash: e.cfg.Hash()}
	if err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := task.Insert(ctx, tx, t); err != nil {
			return err
		}
		_, err := task.Transition(ctx, tx, t.ID, task.Planning, "watch", "", "core")
		return err
	}); err != nil {
		return "", err
	}
	t, _ = task.Get(ctx, e.st.R, t.ID)
	s, err := e.newSession(ctx, t) // grant with no tools: nothing can be invoked
	if err == nil {
		var raw json.RawMessage
		raw, err = e.callWorker(ctx, s, "task.watch", map[string]any{"name": tr.Name, "purpose": tr.Spec.Purpose, "changes": changes})
		if err == nil {
			err = e.storeFindings(ctx, tr, t, raw)
		}
	}
	final, reason := task.Succeeded, ""
	if err != nil {
		final, reason = task.Failed, reasonCode(err)
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		task.Transition(ctx, tx, t.ID, task.Running, "", "", "core")
		if _, err := task.Transition(ctx, tx, t.ID, final, reason, "", "core"); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, t.ID)
	})
	return t.ID, err
}

type finding struct {
	Source            string  `json:"source"`
	ObservedChange    string  `json:"observed_change"`
	Confidence        float64 `json:"confidence"`
	Relevance         string  `json:"relevance"`
	SuggestedNextStep string  `json:"suggested_next_step"`
}

func (e *Engine) storeFindings(ctx context.Context, tr *Trigger, t *task.Task, raw json.RawMessage) error {
	var r struct {
		Findings []finding `json:"findings"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("invalid_watch_result: %v", err)
	}
	allowed := map[string]bool{}
	for _, s := range tr.Spec.Sources {
		allowed[s] = true
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		for _, f := range r.Findings {
			if !allowed[f.Source] {
				continue // a finding must name one of the watched sources
			}
			body, _ := json.Marshal(map[string]any{"source": f.Source, "checked_at": time.Now().UTC().Format(time.RFC3339),
				"observed_change": truncate(f.ObservedChange, 2000), "confidence": f.Confidence, "relevance": f.Relevance,
				"suggested_next_step": truncate(f.SuggestedNextStep, 500), "trust": "untrusted (summary of external content)"})
			tid := t.ID
			if _, err := tx.ExecContext(ctx, "insert into notifications(id, kind, trigger_id, task_id, title, body_json, created_at) values(?,?,?,?,?,?,?)",
				ids.New(), "finding", tr.ID, tid, truncate(tr.Name+": change in "+f.Source, 200), string(body), ids.NowMs()); err != nil {
				return err
			}
		}
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, "FINDINGS_RECORDED", "core", map[string]any{"count": len(r.Findings), "trigger_id": tr.ID})
		return err
	})
}

// Notification is an owner notification.
type Notification struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	TriggerID *string         `json:"trigger_id"`
	TaskID    *string         `json:"task_id"`
	Title     string          `json:"title"`
	Body      json.RawMessage `json:"body"`
	ReadAt    *int64          `json:"read_at"`
	CreatedAt int64           `json:"created_at"`
}

// Notifications lists notifications, unread first.
func (e *Engine) Notifications(ctx context.Context, unreadOnly bool) ([]Notification, error) {
	q := "select id, kind, trigger_id, task_id, title, body_json, read_at, created_at from notifications"
	if unreadOnly {
		q += " where read_at is null"
	}
	rows, err := e.st.R.QueryContext(ctx, q+" order by (read_at is not null), created_at desc limit 200")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var body string
		rows.Scan(&n.ID, &n.Kind, &n.TriggerID, &n.TaskID, &n.Title, &body, &n.ReadAt, &n.CreatedAt)
		n.Body = json.RawMessage(body)
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkRead marks a notification read.
func (e *Engine) MarkRead(ctx context.Context, id string) error {
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update notifications set read_at=? where id=? and read_at is null", ids.NowMs(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return nil
	})
}
