package engine

// Designed screens (ADR-0060). The owner's app has pages whose layout AURA
// itself designs on request ("make my home about approvals first"). A design
// is data, never code: the worker proposes a scene and blocks, the Core
// sanitises them with the same closed lists as a reply view, stores the result
// as a new version and keeps the old ones so the owner can roll back. The Core,
// not the design, decides what is urgent: pending approvals and provider
// refusals are reported in the home context whatever the screen shows.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/view"
)

// ScreenNames are the pages AURA may design.
var ScreenNames = []string{"home"}

const maxScreenSummary = 400

// ErrNoScreen is returned for a screen name outside ScreenNames.
var ErrNoScreen = fmt.Errorf("%w: unknown screen", ErrInvalid)

// ScreenVersion is one stored design of a screen. Version 0 is the Core's
// built-in default and is never stored.
type ScreenVersion struct {
	Name      string      `json:"name"`
	Version   int         `json:"version"`
	Screen    view.Screen `json:"screen"`
	Request   string      `json:"request,omitempty"`
	Summary   string      `json:"summary,omitempty"`
	TaskID    *string     `json:"task_id,omitempty"`
	Active    bool        `json:"active"`
	CreatedAt int64       `json:"created_at,omitempty"`
}

func knownScreen(name string) bool {
	for _, n := range ScreenNames {
		if n == name {
			return true
		}
	}
	return false
}

// DefaultScreen is the built-in design of a screen.
func DefaultScreen(name string) view.Screen {
	return view.Screen{Scene: view.Scene{Layout: "stack"}, Blocks: []view.Block{
		{Type: "widget", Source: "greeting"},
		{Type: "widget", Source: "attention"},
		{Type: "widget", Source: "suggestions", Limit: 6},
		{Type: "widget", Source: "activity", Limit: 4},
		{Type: "widget", Source: "recent", Limit: 5},
		{Type: "widget", Source: "schedules", Limit: 3, Span: "half"},
		{Type: "widget", Source: "skills", Limit: 4, Span: "half"},
	}}
}

func scanScreen(row interface{ Scan(...any) error }) (*ScreenVersion, error) {
	v := &ScreenVersion{}
	var spec string
	var active int
	if err := row.Scan(&v.Name, &v.Version, &spec, &v.Request, &v.Summary, &v.TaskID, &active, &v.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(spec), &v.Screen); err != nil {
		return nil, err
	}
	if v.Screen.Blocks == nil {
		v.Screen.Blocks = []view.Block{}
	}
	v.Active = active == 1
	return v, nil
}

const screenCols = "name, version, spec_json, request, summary, task_id, active, created_at"

// ActiveScreen returns the design in use: the active stored version, or the default.
func (e *Engine) ActiveScreen(ctx context.Context, name string) (*ScreenVersion, error) {
	if !knownScreen(name) {
		return nil, ErrNoScreen
	}
	v, err := scanScreen(e.st.R.QueryRowContext(ctx, "select "+screenCols+" from ui_screens where name=? and active=1", name))
	if errors.Is(err, sql.ErrNoRows) {
		return &ScreenVersion{Name: name, Screen: DefaultScreen(name), Active: true}, nil
	}
	return v, err
}

// ScreenVersions lists the stored designs of a screen, newest first.
func (e *Engine) ScreenVersions(ctx context.Context, name string) ([]*ScreenVersion, error) {
	if !knownScreen(name) {
		return nil, ErrNoScreen
	}
	rows, err := e.st.R.QueryContext(ctx, "select "+screenCols+" from ui_screens where name=? order by version desc limit 30", name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ScreenVersion{}
	for rows.Next() {
		v, err := scanScreen(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// storeScreen saves a design as the new active version.
func (e *Engine) storeScreen(ctx context.Context, name string, sc view.Screen, request, summary, taskID string) (int, error) {
	spec, _ := json.Marshal(sc)
	version := 0
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "select coalesce(max(version),0)+1 from ui_screens where name=?", name).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update ui_screens set active=0 where name=? and active=1", name); err != nil {
			return err
		}
		var tid any
		if taskID != "" {
			tid = taskID
		}
		_, err := tx.ExecContext(ctx, "insert into ui_screens(id, name, version, spec_json, request, summary, task_id, active, created_at) values(?,?,?,?,?,?,?,1,?)",
			ids.New(), name, version, string(spec), truncate(request, 2000), summary, tid, ids.NowMs())
		return err
	})
	return version, err
}

// UseScreen makes a stored version the active one; version 0 returns to the
// built-in default. "previous" semantics live in RollbackScreen.
func (e *Engine) UseScreen(ctx context.Context, name string, version int) (*ScreenVersion, error) {
	if !knownScreen(name) {
		return nil, ErrNoScreen
	}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if version > 0 {
			var n int
			if err := tx.QueryRowContext(ctx, "select count(*) from ui_screens where name=? and version=?", name, version).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return task.ErrNotFound
			}
		}
		if _, err := tx.ExecContext(ctx, "update ui_screens set active=0 where name=? and active=1", name); err != nil {
			return err
		}
		if version > 0 {
			_, err := tx.ExecContext(ctx, "update ui_screens set active=1 where name=? and version=?", name, version)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.ActiveScreen(ctx, name)
}

// RollbackScreen returns to the design before the active one (the default when
// there is none).
func (e *Engine) RollbackScreen(ctx context.Context, name string) (*ScreenVersion, error) {
	cur, err := e.ActiveScreen(ctx, name)
	if err != nil {
		return nil, err
	}
	prev := 0
	if cur.Version > 0 {
		e.st.R.QueryRowContext(ctx, "select coalesce(max(version),0) from ui_screens where name=? and version<?", name, cur.Version).Scan(&prev)
	}
	return e.UseScreen(ctx, name, prev)
}

// designTarget is the screen a task designs: named by the request itself
// (scope) or by the planner for a conversational request.
func (e *Engine) designTarget(ctx context.Context, t *task.Task) string {
	if knownScreen(t.Scope.Design) {
		return t.Scope.Design
	}
	var raw string
	if err := e.st.R.QueryRowContext(ctx, "select state_json from checkpoints where task_id=? and label='plan' order by created_at desc limit 1",
		t.ID).Scan(&raw); err != nil {
		return ""
	}
	var p planDoc
	if json.Unmarshal([]byte(raw), &p) != nil || p.Design == nil || !knownScreen(*p.Design) {
		return ""
	}
	return *p.Design
}

// widgetDocs tells the designer what each live source shows.
var widgetDocs = map[string]string{
	"greeting":      "greeting with the time of day and one line on what is going on",
	"ask":           "the box where the owner types or speaks a request",
	"attention":     "what needs the owner now: approvals, questions, failures (empty when nothing does)",
	"activity":      "tasks AURA is working on right now, live",
	"recent":        "the latest finished requests with their replies",
	"approvals":     "actions waiting for the owner's approval",
	"notifications": "unread notifications from watches and schedules",
	"suggestions":   "requests the owner can send with one tap, chosen for the moment",
	"skills":        "what AURA has learned to do",
	"memories":      "what AURA remembers",
	"schedules":     "scheduled and watching triggers",
	"limitations":   "AURA's known limitations and what is being done about them",
	"self":          "AURA's measured self-model: success rate, strengths",
	"usage":         "today's tasks and cost",
}

// designScreen asks the worker for a new design of a screen, sanitises it and
// makes it the active version. The reply tells the owner what changed.
func (e *Engine) designScreen(ctx context.Context, t *task.Task, name string) error {
	cur, err := e.ActiveScreen(ctx, name)
	if err != nil {
		return err
	}
	s, err := e.toollessSession(ctx, t)
	if err != nil {
		return err
	}
	octx, _ := e.ownerContext(ctx, t)
	raw, err := e.callWorker(ctx, s, "ui.design", map[string]any{"request": e.objectiveFor(ctx, t), "screen": name, "current": cur.Screen,
		"widgets": widgetDocs, "blocks_schema": view.ScreenSchema(), "scene_schema": view.SceneSchema(), "max_blocks": view.MaxScreenBlocks,
		"owner_context": octx, "conversation": e.conversation(ctx, t), "self": e.selfBrief(ctx, t), "tier": tierFor(t)})
	if s.fatal != nil {
		return s.fatal
	}
	if err != nil {
		return err
	}
	var r struct {
		Summary string          `json:"summary"`
		Scene   json.RawMessage `json:"scene"`
		Blocks  json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("invalid_design: %v", err)
	}
	sc, removed := view.SanitizeScreen(r.Scene, r.Blocks)
	if len(sc.Blocks) == 0 {
		return errors.New("invalid_design: the design has no usable block")
	}
	summary := view.CleanText(r.Summary, maxScreenSummary)
	if summary == "" {
		summary = "Tampilan sudah saya ubah. / The screen has been redesigned."
	}
	version, err := e.storeScreen(ctx, name, sc, t.Objective, summary, t.ID)
	if err != nil {
		return err
	}
	types := []string{}
	for _, b := range sc.Blocks {
		types = append(types, strings.TrimSuffix(b.Type+":"+b.Source, ":"))
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, "SCREEN_DESIGNED", "core", map[string]any{"screen": name, "version": version, "blocks": types})
		return err
	})
	notes := []ReplyNote{{Code: "screen_designed", Detail: fmt.Sprintf("%s v%d", name, version)}}
	if len(removed) > 0 {
		notes = append(notes, ReplyNote{Code: "view_trimmed", Detail: truncate(strings.Join(removed, "; "), 400)})
	}
	return e.storeReply(ctx, t, summary, nil, sc.Scene, BasisModel, notes, nil)
}

// toollessSession is a worker session whose grant carries no tool: writing a
// reply or a design needs none.
func (e *Engine) toollessSession(ctx context.Context, t *task.Task) (*session, error) {
	gen, err := e.supFor(t.ID).Ensure()
	if err != nil {
		return nil, err
	}
	s := &session{t: t, gen: gen}
	expires := time.Now().Add(time.Duration(t.Budget.MaxWallSeconds) * time.Second).UnixMilli()
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		s.token, s.grant, err = policy.Mint(ctx, tx, t.ID, gen, policy.Scopes{}, expires, nil)
		return err
	})
	return s, err
}

// HomeContext is what the Core knows about this moment. Clients use it to
// decide the look of the home screen and what to put first; it is computed
// from the Core's records, never by a model.
type HomeContext struct {
	Now          string     `json:"now"`     // RFC 3339, workspace time zone
	DayPart      string     `json:"daypart"` // dawn | morning | midday | afternoon | evening | night
	Approvals    int        `json:"approvals"`
	Questions    int        `json:"questions"`
	Running      int        `json:"running"`
	Unread       int        `json:"unread"`
	FailedToday  int        `json:"failed_today"`
	DoneToday    int        `json:"done_today"`
	CostTodayUSD float64    `json:"cost_today_usd"`
	Providers    []string   `json:"provider_issues"` // provider ids that currently refuse calls
	Scene        view.Scene `json:"scene"`           // the look the Core suggests for this moment
	Headline     string     `json:"headline"`        // code: needs_approval | needs_answer | provider_problem | working | failed | unread | idle
}

func dayPart(h int) string {
	switch {
	case h < 4:
		return "night"
	case h < 6:
		return "dawn"
	case h < 11:
		return "morning"
	case h < 15:
		return "midday"
	case h < 18:
		return "afternoon"
	case h < 22:
		return "evening"
	}
	return "night"
}

var dayAccent = map[string]string{"dawn": "violet", "morning": "amber", "midday": "ocean", "afternoon": "emerald", "evening": "rose", "night": "aurora"}

// Home computes the context of this moment.
func (e *Engine) Home(ctx context.Context) HomeContext {
	now := time.Now().In(e.cfg.Location())
	c := HomeContext{Now: now.Format(time.RFC3339), DayPart: dayPart(now.Hour()), Providers: []string{}}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	r := e.st.R
	r.QueryRowContext(ctx, "select count(*) from approvals where status='pending' and expires_at>?", now.UnixMilli()).Scan(&c.Approvals)
	r.QueryRowContext(ctx, "select count(*) from tasks where parent_task_id is null and state='WAITING_INPUT'").Scan(&c.Questions)
	r.QueryRowContext(ctx, "select count(*) from tasks where parent_task_id is null and state in ('PLANNING','RUNNING','VERIFYING','QUEUED')").Scan(&c.Running)
	r.QueryRowContext(ctx, "select count(*) from notifications where read_at is null and (snoozed_until is null or snoozed_until<=strftime('%s','now')*1000) and (expires_at is null or expires_at>strftime('%s','now')*1000)").Scan(&c.Unread)
	r.QueryRowContext(ctx, `select coalesce(sum(state='FAILED'),0), coalesce(sum(state='COMPLETED'),0), coalesce(sum(cost_actual_usd),0) from tasks
		where parent_task_id is null and updated_at>=?`, day).Scan(&c.FailedToday, &c.DoneToday, &c.CostTodayUSD)
	if e.gw != nil {
		for _, i := range e.gw.ProviderIssues() {
			c.Providers = append(c.Providers, i.Provider)
		}
	}
	c.Scene = view.Scene{Mood: "calm", Accent: dayAccent[c.DayPart], Layout: "stack"}
	switch {
	case c.Approvals > 0:
		c.Headline, c.Scene.Mood, c.Scene.Accent = "needs_approval", "alert", "amber"
	case c.Questions > 0:
		c.Headline, c.Scene.Mood = "needs_answer", "focus"
	case len(c.Providers) > 0:
		c.Headline, c.Scene.Mood, c.Scene.Accent = "provider_problem", "alert", "rose"
	case c.Running > 0:
		c.Headline, c.Scene.Mood = "working", "focus"
	case c.FailedToday > 0 && c.DoneToday == 0:
		c.Headline = "failed"
	case c.Unread > 0:
		c.Headline, c.Scene.Mood = "unread", "explore"
	default:
		c.Headline = "idle"
	}
	return c
}
