package engine

// AUL workflows (Blueprint D.1 P8, ADR-0050). The owner registers a workflow;
// the Core compiles and checks it (declared capabilities, literal URLs and
// paths) and stores its IR with a sha256. Running it creates a task whose grant
// is exactly the declared capabilities; the program runs in the AUL VM inside
// WebAssembly, and every tool call comes back to the Core, which checks it
// again against the task's grant and the declaration before doing anything.
// Workflows never ask for approvals: an A2 action is refused.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"aura.local/core/internal/aul"
	"aura.local/core/internal/aul/ir"
	"aura.local/core/internal/budget"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

// ErrAULUnavailable means the AUL VM module is not configured.
var ErrAULUnavailable = errors.New("aul_unavailable")

// Workflow is a registered AUL workflow version.
type Workflow struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Version   int          `json:"version"`
	Source    string       `json:"source"`
	SHA256    string       `json:"sha256"`
	Requires  []ir.Require `json:"requires"`
	Inputs    []ir.Input   `json:"inputs"`
	Status    string       `json:"status"`
	CreatedBy string       `json:"created_by"`
	CreatedAt int64        `json:"created_at"`
	program   *ir.Program
}

// CheckWorkflow compiles source and reports diagnostics; nothing is stored.
func (e *Engine) CheckWorkflow(source string) (*ir.Program, []aul.Diagnostic) {
	return aul.Compile(source)
}

// RegisterWorkflow compiles and stores a new version (owner action). The
// version in the source must be higher than any registered version of that
// name; re-registering identical IR is a no-op.
func (e *Engine) RegisterWorkflow(ctx context.Context, source, by string) (*Workflow, []aul.Diagnostic, error) {
	p, diags := aul.Compile(source)
	if diags != nil {
		return nil, diags, fmt.Errorf("%w: the workflow does not compile", ErrInvalid)
	}
	irb, _ := json.Marshal(p)
	hash, reqs := aul.Hash(p), js(p.Requires)
	var id string
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var maxV int
		var sameID, sameHash string
		tx.QueryRowContext(ctx, "select coalesce(max(version),0) from aul_workflows where name=?", p.Name).Scan(&maxV)
		tx.QueryRowContext(ctx, "select id, sha256 from aul_workflows where name=? and version=?", p.Name, p.Version).Scan(&sameID, &sameHash)
		if sameID != "" {
			if sameHash == hash {
				id = sameID
				return nil
			}
			return fmt.Errorf("%w: %s version %d exists with different content; raise the version", ErrInvalid, p.Name, p.Version)
		}
		if p.Version <= maxV {
			return fmt.Errorf("%w: %s version must be higher than %d", ErrInvalid, p.Name, maxV)
		}
		now := ids.NowMs()
		id = ids.New()
		if _, err := tx.ExecContext(ctx, "update aul_workflows set status='superseded', updated_at=? where name=? and status='active'", now, p.Name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into aul_workflows(id, name, version, source, ir_json, sha256, requires_json, status, created_by,
			created_at, updated_at) values(?,?,?,?,?,?,?,'active',?,?,?)`, id, p.Name, p.Version, source, string(irb), hash, reqs, by, now, now); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "AUL_WORKFLOW_REGISTERED", by, "", "", map[string]any{"id": id, "name": p.Name, "version": p.Version,
			"sha256": hash, "requires": p.Requires})
	})
	if err != nil {
		return nil, nil, err
	}
	w, err := e.GetWorkflow(ctx, id)
	return w, nil, err
}

const wfCols = "id, name, version, source, ir_json, sha256, status, created_by, created_at"

func scanWorkflow(r rowScanner) (*Workflow, error) {
	var w Workflow
	var irj string
	if err := r.Scan(&w.ID, &w.Name, &w.Version, &w.Source, &irj, &w.SHA256, &w.Status, &w.CreatedBy, &w.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, task.ErrNotFound
		}
		return nil, err
	}
	var p ir.Program
	if err := json.Unmarshal([]byte(irj), &p); err != nil {
		return nil, fmt.Errorf("stored workflow %s is unreadable: %w", w.ID, err)
	}
	w.program, w.Requires, w.Inputs = &p, p.Requires, p.Inputs
	return &w, nil
}

// GetWorkflow returns a workflow by ID, or the active version by name.
func (e *Engine) GetWorkflow(ctx context.Context, idOrName string) (*Workflow, error) {
	w, err := scanWorkflow(e.st.R.QueryRowContext(ctx, "select "+wfCols+" from aul_workflows where id=?", idOrName))
	if errors.Is(err, task.ErrNotFound) {
		w, err = scanWorkflow(e.st.R.QueryRowContext(ctx, "select "+wfCols+" from aul_workflows where name=? and status='active'", idOrName))
	}
	return w, err
}

// ListWorkflows lists all versions, newest first.
func (e *Engine) ListWorkflows(ctx context.Context) ([]*Workflow, error) {
	rows, err := e.st.R.QueryContext(ctx, "select "+wfCols+" from aul_workflows order by name, version desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Workflow
	for rows.Next() {
		w, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DisableWorkflow stops a workflow version from running.
func (e *Engine) DisableWorkflow(ctx context.Context, id, by string) (*Workflow, error) {
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update aul_workflows set status='disabled', updated_at=? where id=? and status != 'disabled'", ids.NowMs(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return policy.Audit(ctx, tx, "AUL_WORKFLOW_DISABLED", by, "", "", map[string]any{"id": id})
	})
	if err != nil {
		return nil, err
	}
	return e.GetWorkflow(ctx, id)
}

// RunWorkflow starts a task that runs the active version of a workflow. Its
// grant is exactly the declared capabilities.
func (e *Engine) RunWorkflow(ctx context.Context, name string, inputs map[string]any, b task.Budget) (*task.Task, error) {
	if e.aul == nil {
		return nil, ErrAULUnavailable
	}
	w, err := e.GetWorkflow(ctx, name)
	if err != nil {
		return nil, err
	}
	if w.Status != "active" {
		return nil, fmt.Errorf("%w: workflow %s is %s", ErrInvalid, w.ID, w.Status)
	}
	for _, in := range w.Inputs {
		if _, ok := inputs[in.Name]; !ok {
			return nil, fmt.Errorf("%w: missing input %s (%s)", ErrInvalid, in.Name, in.Type)
		}
	}
	for k := range inputs {
		if !slices.ContainsFunc(w.Inputs, func(in ir.Input) bool { return in.Name == k }) {
			return nil, fmt.Errorf("%w: unknown input %s", ErrInvalid, k)
		}
	}
	inb, _ := json.Marshal(inputs)
	if len(inb) > 256<<10 {
		return nil, fmt.Errorf("%w: inputs larger than 256 KiB", ErrInvalid)
	}
	var toolIDs, domains []string
	for _, r := range w.Requires {
		toolIDs = append(toolIDs, r.Tool)
		domains = append(domains, r.Domains...)
	}
	sort.Strings(toolIDs)
	t, err := e.validate(CreateInput{Objective: fmt.Sprintf("[workflow] %s v%d", w.Name, w.Version), Tools: toolIDs, AllowDomains: domains, Budget: b})
	if err != nil {
		return nil, err
	}
	// Role "workflow": the owner's program, not AURA's reasoning, so it stays out
	// of the self-model, the Limitation Ledger and canary comparisons.
	t.Scope.Workflow, t.AgentRole = w.ID, "workflow"
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := task.Insert(ctx, tx, t); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "insert into aul_runs(task_id, workflow_id, inputs_json) values(?,?,?)", t.ID, w.ID, string(inb))
		return err
	})
	if err != nil {
		return nil, err
	}
	e.Wake()
	return task.Get(ctx, e.st.R, t.ID)
}

// runWorkflow is runTask for a workflow task.
func (e *Engine) runWorkflow(ctx context.Context, t *task.Task) error {
	if e.aul == nil {
		return ErrAULUnavailable
	}
	w, err := e.GetWorkflow(ctx, t.Scope.Workflow)
	if err != nil {
		return err
	}
	if aul.Hash(w.program) != w.SHA256 {
		return fmt.Errorf("aul_integrity: stored IR of %s does not match its sha256", w.ID)
	}
	if w.Status == "disabled" {
		return fmt.Errorf("aul_disabled: workflow %s was disabled", w.ID)
	}
	var inj string
	if err := e.st.R.QueryRowContext(ctx, "select inputs_json from aul_runs where task_id=?", t.ID).Scan(&inj); err != nil {
		return err
	}
	var inputs map[string]any
	json.Unmarshal([]byte(inj), &inputs)
	if t.State == task.Planning {
		if err := e.transition(ctx, t.ID, task.Running, "", ""); err != nil {
			return err
		}
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, "AUL_STARTED", "core", map[string]any{"workflow": w.ID, "name": w.Name, "version": w.Version,
			"sha256": w.SHA256, "vm_sha256": e.aul.SHA256})
		return err
	})
	h := &aulHost{e: e, t: t, w: w}
	res, err := e.aul.Run(ctx, w.program, inputs, h)
	calls := 0
	if res != nil {
		calls = res.Calls
	}
	if h.halted {
		return budget.ErrHalted
	}
	if err != nil {
		e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "update aul_runs set calls=?, finished_at=? where task_id=?", calls, ids.NowMs(), t.ID)
			return err
		})
		code := strings.SplitN(err.Error(), ":", 2)[0]
		return fmt.Errorf("%s: %v", code, err)
	}
	out, _ := json.Marshal(res.Output)
	if len(out) > 64<<10 {
		out, _ = json.Marshal(map[string]any{"truncated": true, "bytes": len(out)})
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "update aul_runs set output_json=?, calls=?, finished_at=? where task_id=?", string(out), calls, ids.NowMs(), t.ID); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, t.ID, nil, "AUL_COMPLETED", "core", map[string]any{"calls": calls, "output": json.RawMessage(out)}); err != nil {
			return err
		}
		if _, err := task.Transition(ctx, tx, t.ID, task.Succeeded, "", "", "core"); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, t.ID)
	})
}

// aulHost is the Core side of every workflow tool call.
type aulHost struct {
	e      *Engine
	t      *task.Task
	w      *Workflow
	halted bool
}

func (h *aulHost) declared(tool string) (ir.Require, bool) {
	for _, r := range h.w.Requires {
		if r.Tool == tool {
			return r, true
		}
	}
	return ir.Require{}, false
}

func (h *aulHost) Call(ctx context.Context, tool string, args map[string]any) (out any, err error) {
	e, t := h.e, h.t
	target := ""
	defer func() {
		data := map[string]any{"tool_id": tool, "target": target, "ok": err == nil}
		if err != nil {
			data["error"] = truncate(err.Error(), 200)
		}
		e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
			_, werr := task.AppendEvent(ctx, tx, t.ID, nil, "AUL_CALL", "core", data)
			return werr
		})
	}()
	deny := func(reason string) error {
		e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, "CAPABILITY_DENIED", "aul", t.ID, "", map[string]any{"reason": reason, "tool_id": tool, "workflow": h.w.ID})
		})
		return errors.New(reason)
	}
	if e.gov.Halted() {
		h.halted = true
		return nil, errors.New("kill_switch")
	}
	r, ok := h.declared(tool)
	spec, known := policy.Lookup(tool)
	if !ok || !known || !slices.Contains(t.Scope.Tools, tool) {
		return nil, deny("tool_not_granted")
	}
	want := ir.ToolArgs[tool]
	if len(args) != len(want) {
		return nil, deny("invalid_arguments")
	}
	str := func(k string) (string, bool) { s, ok := args[k].(string); return s, ok }
	for _, k := range want {
		if _, ok := str(k); !ok {
			return nil, deny("invalid_arguments")
		}
	}
	if spec.Tier(args) != policy.A0 {
		return nil, deny("approval_required") // workflows never ask the owner
	}
	switch tool {
	case "web.fetch":
		raw, _ := str("url")
		target = raw
		u, perr := url.Parse(raw)
		if perr != nil || !aul.URLAllowed(raw, r.Domains) || tools.CheckURL(u, t.Scope.AllowDomains) != nil {
			return nil, deny("domain_not_allowed")
		}
		res, err := e.fetch.Fetch(ctx, raw, t.Scope.AllowDomains)
		if err != nil {
			return nil, fmt.Errorf("tool_failed: %v", err)
		}
		if res.Status >= 400 {
			return nil, fmt.Errorf("tool_failed: HTTP %d from %s", res.Status, res.FinalURL)
		}
		return map[string]any{"url": res.FinalURL, "status": float64(res.Status), "title": res.Title, "text": truncate(res.Text, 200000),
			"sha256": res.SHA256}, nil
	case "workspace.read":
		p, _ := str("path")
		target = p
		if !aul.PathAllowed(p, r.Paths) {
			return nil, deny("path_not_allowed")
		}
		res, err := e.ws.Read(t.ID, p)
		if err != nil {
			return nil, fmt.Errorf("tool_failed: %v", err)
		}
		return map[string]any{"path": res.Path, "content": res.Content, "sha256": res.SHA256}, nil
	case "workspace.write":
		p, _ := str("path")
		c, _ := str("content")
		target = p
		if !aul.PathAllowed(p, r.Paths) {
			return nil, deny("path_not_allowed")
		}
		if len(c) > 1<<20 {
			return nil, deny("content_too_large")
		}
		res, err := e.ws.Write(t.ID, p, c)
		if err != nil {
			return nil, fmt.Errorf("tool_failed: %v", err)
		}
		return map[string]any{"path": res.Path, "bytes": float64(res.Bytes), "sha256": res.SHA256}, nil
	}
	return nil, deny("tool_not_granted")
}
