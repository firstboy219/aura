// Package engine drives tasks end to end (Blueprint E.2 items 1, 8, 11): it
// asks the worker for a plan, executes steps sequentially, serves the worker's
// requests through policy → budget → gateway/tools, records every outcome as an
// event, recovers after a crash by side-effect class, and runs the kill switch.
// One task runs at a time (ADR-0003).
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/mcp"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/rpc"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/worker"
)

const maxStepAttempts = 3

// Engine owns task execution.
type Engine struct {
	cfg         *config.Config
	st          *store.Store
	gw          *gateway.Gateway
	gov         *budget.Governor
	sup         *worker.Supervisor
	fetch       *tools.Fetcher
	ws          *tools.Workspace
	srch        tools.Searcher // nil: web.search unavailable
	mcp         map[string]*mcp.Client
	docs        map[string]string // owner-written descriptions of MCP tools
	charter     string            // owner Charter text ("" = not provided)
	ledgerCfg   LedgerConfig
	version     string
	lastImprove time.Time
	log         *slog.Logger
	runID       string

	wake    chan struct{}
	mu      sync.Mutex
	current string             // task being driven
	cancel  context.CancelFunc // cancels the current task's context
	why     string             // reason for cancel: pause | cancel | kill_switch
}

// Deps are the engine's collaborators.
type Deps struct {
	Config    *config.Config
	Store     *store.Store
	Gateway   *gateway.Gateway
	Governor  *budget.Governor
	Worker    *worker.Supervisor
	Fetcher   *tools.Fetcher
	Workspace *tools.Workspace
	Searcher  tools.Searcher
	MCP       map[string]*mcp.Client // by server name
	ToolDocs  map[string]string      // tool id -> owner-written description (MCP tools)
	Version   string
	Charter   string // owner Charter text, verified against its pinned hash by the caller
	Log       *slog.Logger
}

func New(d Deps) *Engine {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Engine{cfg: d.Config, st: d.Store, gw: d.Gateway, gov: d.Governor, sup: d.Worker, fetch: d.Fetcher,
		ws: d.Workspace, srch: d.Searcher, mcp: d.MCP, docs: d.ToolDocs, version: d.Version, charter: d.Charter, ledgerCfg: ledgerFrom(d.Config), log: d.Log, runID: ids.New(), wake: make(chan struct{}, 1)}
}

// Wake nudges the scheduler loop.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Run recovers interrupted work and then drives runnable tasks until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.Recover(ctx); err != nil {
		return err
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if !e.gov.Halted() {
			for {
				t, err := task.Runnable(ctx, e.st.R)
				if err != nil {
					e.log.Error("scheduler", "err", err)
					break
				}
				if t == nil || ctx.Err() != nil || e.gov.Halted() {
					break
				}
				e.drive(ctx, t)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-e.wake:
		case <-tick.C:
			e.ExpireApprovals(ctx)
		}
		e.Supervise(ctx)
		e.RunTriggers(ctx)
		e.improveTick(ctx)
	}
}

// Recover handles tasks a previous Core run left in flight (ADR-0013 lease):
// steps that had started are resolved by side-effect class before resuming.
func (e *Engine) Recover(ctx context.Context) error {
	// A watch run interrupted by a crash is simply failed; the next scheduled
	// run checks the sources again.
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "update tasks set state='FAILED', reason='interrupted_watch', updated_at=? where agent_role='watch' and state in ('CREATED','PLANNING','RUNNING')", ids.NowMs())
		return err
	})
	rows, err := e.st.R.QueryContext(ctx, "select id from tasks where state in ('PLANNING','RUNNING') and agent_role not in ('watch','improvement') and coalesce(lease_owner,'') != ?", e.runID)
	if err != nil {
		return err
	}
	var idsToRecover []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		idsToRecover = append(idsToRecover, id)
	}
	rows.Close()
	for _, id := range idsToRecover {
		steps, err := task.Steps(ctx, e.st.R, id)
		if err != nil {
			return err
		}
		err = e.st.Write(ctx, func(tx *sql.Tx) error {
			for _, s := range steps {
				if s.State != task.StepStarted {
					continue
				}
				action := "rerun"
				if s.SideEffect == "write_external" {
					action = "outcome_unknown" // never blindly retried (no P1 tool has this class)
				} else if s.SideEffect == "write_idempotent" && s.OutcomeRef != nil {
					action = "replay_recorded_intent"
				}
				if _, err := task.AppendEvent(ctx, tx, id, &s.ID, "STEP_RECOVERED", "core", map[string]any{
					"step_no": s.StepNo, "side_effect": s.SideEffect, "action": action, "idempotency_key": s.IdempotencyKey}); err != nil {
					return err
				}
				if action == "outcome_unknown" {
					if err := e.outcomeUnknown(ctx, tx, id, s); err != nil {
						return err
					}
					continue
				}
				if action == "rerun" {
					// The attempt was interrupted by the Core, not failed by the step:
					// it does not count against max attempts (recoveries are capped separately).
					if _, err := tx.ExecContext(ctx, "update task_steps set state='PENDING', attempts=max(0, attempts-1) where id=?", s.ID); err != nil {
						return err
					}
				}
			}
			_, err := task.AppendEvent(ctx, tx, id, nil, "TASK_RECOVERED", "core", map[string]any{"run_id": e.runID})
			if err != nil {
				return err
			}
			// A step whose external outcome is unknown parked the task for the owner.
			var waiting int
			tx.QueryRowContext(ctx, "select count(*) from task_steps where task_id=? and state='WAITING_APPROVAL'", id).Scan(&waiting)
			if waiting > 0 {
				if _, err := task.Transition(ctx, tx, id, task.WaitingApproval, "OUTCOME_UNKNOWN", "outcome_unknown", "core"); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(idsToRecover) > 0 {
		e.log.Info("recovered tasks", "count", len(idsToRecover))
	}
	return nil
}

// drive runs one task until it is terminal, waiting, paused, or interrupted.
func (e *Engine) drive(parent context.Context, t *task.Task) {
	wall := time.Duration(t.Budget.MaxWallSeconds) * time.Second
	start := ids.NowMs() // queue time is not work time: the wall budget starts when the task starts
	if t.StartedAt != nil {
		start = *t.StartedAt
	}
	deadline := time.UnixMilli(start).Add(wall)
	ctx, cancel := context.WithDeadline(parent, deadline)
	e.mu.Lock()
	e.current, e.cancel, e.why = t.ID, cancel, ""
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		e.current, e.cancel = "", nil
		e.mu.Unlock()
	}()

	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		return task.SetLease(ctx, tx, t.ID, e.runID, deadline.UnixMilli())
	})
	if err == nil {
		err = e.runTask(ctx, t)
	}
	e.finish(parent, t.ID, err)
}

// finish maps an execution error to the task's next state.
func (e *Engine) finish(ctx context.Context, id string, err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	why := e.why
	e.mu.Unlock()
	to, reason, wait := task.Failed, reasonCode(err), ""
	switch {
	case why != "":
		return // pause, cancel and kill switch already set the task's state

	case errors.Is(err, context.DeadlineExceeded):
		to, reason = task.BudgetExceeded, "max_wall_seconds"
	case errors.Is(err, budget.ErrTaskBudget), errors.Is(err, budget.ErrDailyBudget), errors.Is(err, budget.ErrIterations):
		to = task.BudgetExceeded
	case errors.Is(err, budget.ErrHalted):
		to, reason, wait = task.Paused, "kill_switch", "kill_switch"
	case errors.Is(err, errStop):
		return
	case ctx.Err() != nil:
		return // Core shutting down: leave the task for recovery
	}
	cause := err // the closure below shadows err
	werr := e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		cur, err := task.Get(ctx, tx, id)
		if err != nil || cur.State.Terminal() || cur.State == to {
			return err
		}
		if !task.CanTransition(cur.State, to) {
			// Never leave a task stuck in a runnable state (the scheduler would
			// pick it again forever): fall back to FAILED with the reason.
			if !task.CanTransition(cur.State, task.Failed) {
				return nil
			}
			to, wait = task.Failed, ""
		}
		if _, err := task.Transition(ctx, tx, id, to, reason, wait, "core"); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, id, nil, "TASK_ERROR", "core", map[string]any{
			"reason": reason, "detail": truncate(cause.Error(), 500)}); err != nil {
			return err
		}
		if to.Terminal() {
			return policy.RevokeTask(ctx, tx, id)
		}
		return nil
	})
	if werr != nil {
		e.log.Error("finish task", "task", id, "err", werr)
	}
	e.log.Info("task stopped", "task", id, "state", to, "reason", reason)
}

// errStop means the task reached a non-error stopping point (waiting/terminal).
var errStop = errors.New("stop")

func reasonCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		if rc, ok := re.Data["reason_code"].(string); ok && rc != "" {
			return rc
		}
	}
	for _, known := range []error{gateway.ErrInvalidOutput, gateway.ErrAllRoutes, gateway.ErrNoRoute, budget.ErrTaskBudget,
		budget.ErrDailyBudget, budget.ErrIterations, worker.ErrCrashed, worker.ErrProtocol} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, ':'); i > 0 && i < 40 && !strings.Contains(msg[:i], " ") {
		return msg[:i]
	}
	return "error"
}

func (e *Engine) transition(ctx context.Context, id string, to task.State, reason, wait string) error {
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.Transition(ctx, tx, id, to, reason, wait, "core")
		return err
	})
}

// session is one Core→worker call context: the grant and the current step.
type session struct {
	t          *task.Task
	token      string
	grant      *policy.Grant
	gen        int64
	step       *task.Step
	plan       *plannedStep
	invoked    bool   // a tool call was accepted for this step
	executed   bool   // the accepted tool call succeeded
	approvalID string // set when the call was queued for approval
	waitID     string // set when the step parked the task on a supervisor wait
	fatal      error  // a Core-side error (budget, halt) that ends the task
	denied     bool   // a request in this call was denied by policy: no replanning around it
}

func (e *Engine) newSession(ctx context.Context, t *task.Task) (*session, error) {
	gen, err := e.sup.Ensure()
	if err != nil {
		return nil, err
	}
	s := &session{t: t, gen: gen}
	sc := policy.Scopes{Tools: t.Scope.Tools, AllowDomains: t.Scope.AllowDomains, PathPrefix: ""}
	expires := time.Now().Add(time.Duration(t.Budget.MaxWallSeconds) * time.Second).UnixMilli()
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		s.token, s.grant, err = policy.Mint(ctx, tx, t.ID, gen, sc, expires, nil)
		return err
	})
	return s, err
}

func (e *Engine) meta(s *session) rpc.Meta {
	m := rpc.Meta{TaskID: s.t.ID, TraceID: s.t.ID, CorrelationID: ids.New(), CapabilityToken: s.token}
	if dl, ok := deadlineOf(s); ok {
		m.DeadlineUnixMs = dl
	}
	if s.step != nil {
		m.StepID = s.step.ID
	}
	return m
}

func deadlineOf(s *session) (int64, bool) {
	start := ids.NowMs()
	if s.t.StartedAt != nil {
		start = *s.t.StartedAt
	}
	return start + int64(s.t.Budget.MaxWallSeconds)*1000, true
}

func (e *Engine) callWorker(ctx context.Context, s *session, method string, params any) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(e.cfg.Worker.CallTimeoutSeconds)*time.Second)
	defer cancel()
	if m, ok := params.(map[string]any); ok {
		m["identity"] = Identity
		if e.charter != "" {
			m["charter"] = e.charter
		}
	}
	raw, err := e.sup.Call(cctx, method, e.meta(s), params, func(hctx context.Context, req *rpc.Envelope) (any, *rpc.Error) {
		return e.serve(hctx, s, req)
	})
	if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("worker_timeout: %w", err)
	}
	return raw, err
}

// outputRe finds workspace files named in an objective (e.g. reports/summary.md).
// A path inside a URL never matches: it must start after whitespace, a quote
// or "(". Keep in sync with OUTPUT_RE in the Python worker.
var outputRe = regexp.MustCompile(`(?:^|[\s"'(])((?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_-]+\.(?:md|txt|json|csv|html|yaml|yml))\b`)

// ExpectedOutputs lists the workspace files an objective explicitly asks for.
// A task that does not produce them is not complete, whatever its plan said.
// Paths in inputs (attachments, files the plan only reads) are not outputs.
func ExpectedOutputs(objective string, inputs ...map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, in := range inputs {
		for k := range in {
			seen[k] = true
		}
	}
	for _, m := range outputRe.FindAllStringSubmatch(objective, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// writeIntent is the write-ahead record of a workspace.write (Blueprint §8.1).
type writeIntent struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

func (e *Engine) checkpointJSON(ctx context.Context, id *string, v any) error {
	if id == nil {
		return sql.ErrNoRows
	}
	var raw string
	if err := e.st.R.QueryRowContext(ctx, "select state_json from checkpoints where id=?", *id).Scan(&raw); err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

func putCheckpoint(ctx context.Context, tx *sql.Tx, taskID, stepID, label string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	id := ids.New()
	var sid any
	if stepID != "" {
		sid = stepID
	}
	_, err = tx.ExecContext(ctx, `insert into checkpoints(id, task_id, step_id, label, state_json, sha256, created_at, purge_after)
		values(?,?,?,?,?,?,?,?)`, id, taskID, sid, label, string(b), tools.Hash(b), ids.NowMs(), ids.NowMs()+30*24*3600*1000)
	return id, err
}
