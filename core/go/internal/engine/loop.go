package engine

// The P2 agent loop (ADR-0016): plan (or ask the owner one question) → review
// complex plans → execute steps → replan after a failed step → deterministic
// checks → verification against the completion criteria → replan or finish.
// Replanning is bounded; every decision is an event.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

const (
	maxReplans   = 2
	maxQuestions = 2
	maxPlanSteps = 20
)

// plannedStep is one planned step with the arguments known at planning time.
// Fields a tool does not use are null.
type plannedStep struct {
	StepNo      int      `json:"step_no"`
	Description string   `json:"description"`
	ToolID      *string  `json:"tool_id"`
	URL         *string  `json:"url"`
	Path        *string  `json:"path"`
	Query       *string  `json:"query"`
	Expression  *string  `json:"expression"`
	Key         *string  `json:"key"`
	Until       *string  `json:"until"`
	Brief       *string  `json:"brief"`
	Tools       []string `json:"tools"`
	Input       *string  `json:"input"` // JSON object text: arguments of an MCP tool
}

type planDoc struct {
	Steps              []plannedStep `json:"steps"`
	CompletionCriteria []string      `json:"completion_criteria"`
	Question           *string       `json:"question"`
}

func decodePlan(raw json.RawMessage) (*planDoc, error) {
	var p planDoc
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("invalid_plan: %v", err)
	}
	return &p, nil
}

func (e *Engine) runTask(ctx context.Context, t *task.Task) error {
	cur, err := task.Get(ctx, e.st.R, t.ID)
	if err != nil {
		return err
	}
	if cur.State == task.Created {
		if err := e.transition(ctx, t.ID, task.Planning, "", ""); err != nil {
			return err
		}
		cur.State = task.Planning
	}
	if cur.Scope.Workflow != "" {
		return e.runWorkflow(ctx, cur) // an AUL workflow runs instead of a plan (ADR-0050)
	}
	if cur.State == task.Planning {
		if err := e.plan(ctx, cur); err != nil {
			return err
		}
	}
	return e.execute(ctx, cur)
}

// toolDocs are owner-written descriptions of the task's MCP tools.
func (e *Engine) toolDocs(t *task.Task) map[string]string {
	out := e.l2Docs(context.Background(), t.Scope.Tools)
	if d := e.specialistDocs(); d != "" && slices.Contains(t.Scope.Tools, "agent.delegate") {
		out["agent.delegate"] = d
	}
	for _, id := range t.Scope.Tools {
		if d, ok := e.docs[id]; ok {
			out[id] = d
		}
	}
	return out
}

// tierFor returns the owner's tier override for a task ("" = cascade policy).
func tierFor(t *task.Task) string {
	if t.TierOverride != nil {
		return *t.TierOverride
	}
	return ""
}

// ownerContext is what the owner told this task: answers and corrections.
// It is trusted as the owner's words (not as instructions to bypass policy).
func (e *Engine) ownerContext(ctx context.Context, t *task.Task) ([]map[string]string, error) {
	out := []map[string]string{}
	rows, err := e.st.R.QueryContext(ctx, "select question, answer from task_inputs where task_id=? and status='answered' order by created_at", t.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q, a string
		rows.Scan(&q, &a)
		out = append(out, map[string]string{"kind": "answer", "question": q, "answer": a})
	}
	rows.Close()
	rows, err = e.st.R.QueryContext(ctx, "select question from task_inputs where task_id=? and status='timed_out' order by created_at", t.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q string
		rows.Scan(&q)
		out = append(out, map[string]string{"kind": "unanswered", "question": q, "answer": noAnswer})
	}
	rows.Close()
	rows, err = e.st.R.QueryContext(ctx, "select kind, content from shared_context where root_task_id=? and trust='owner' order by id", t.RootTaskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, c string
		rows.Scan(&k, &c)
		out = append(out, map[string]string{"kind": k, "content": c})
	}
	return out, rows.Err()
}

func (e *Engine) plan(ctx context.Context, t *task.Task) error {
	s, err := e.newSession(ctx, t)
	if err != nil {
		return err
	}
	octx, err := e.ownerContext(ctx, t)
	if err != nil {
		return err
	}
	var asked, unanswered int
	e.st.R.QueryRowContext(ctx, "select count(*), coalesce(sum(status='timed_out'),0) from task_inputs where task_id=?", t.ID).Scan(&asked, &unanswered)
	mayAsk := asked < maxQuestions && unanswered == 0 && t.ParentTaskID == nil // an owner who did not answer is not asked again
	guidance, variants := e.brainGuidance(ctx, t)
	if len(variants) > 0 {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, t.ID, nil, "BRAIN_GUIDANCE", "core", map[string]any{"variants": variants})
			return err
		})
	}
	// An owner task may be answered without tools, and is planned knowing the
	// recent conversation and what AURA is (ADR-0055).
	mayAnswer := e.mayAnswer(t)
	var conversation []map[string]string
	self := ""
	if e.wantsReply(t) {
		conversation, self = e.conversation(ctx, t), e.selfBrief(ctx, t)
	}
	raw, err := e.callWorker(ctx, s, "task.plan", map[string]any{"guidance": guidance,
		"objective": t.Objective, "granted_tools": t.Scope.Tools, "allow_domains": t.Scope.AllowDomains,
		"budget": t.Budget, "owner_context": octx, "may_ask": mayAsk, "tier": tierFor(t),
		"attachments": t.Scope.Attachments, "tool_docs": e.toolDocs(t), "memory": e.memoryContext(ctx, t),
		"skills": e.skillContext(ctx, t), "profile": e.profileContext(ctx, t),
		"may_answer": mayAnswer, "conversation": conversation, "self": self})
	if s.fatal != nil {
		return s.fatal
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	p, err := decodePlan(raw)
	if err != nil {
		return err
	}
	inReply := false
	if p.Question != nil && strings.TrimSpace(*p.Question) != "" && mayAnswer {
		// A conversational request (nothing a tool must produce) is never parked
		// waiting for an answer: the reply itself asks, and the owner's next message
		// continues the conversation (ADR-0055 addendum 2026-11-11). Parking turned
		// greetings into tasks that waited two days for an answer nobody owed.
		p.Question, p.Steps, inReply = nil, nil, true
	}
	if p.Question != nil && strings.TrimSpace(*p.Question) != "" {
		if asked >= maxQuestions {
			return fmt.Errorf("invalid_plan: planner asked more than %d questions", maxQuestions)
		}
		return e.askOwner(ctx, t, "", *p.Question)
	}
	var steps []task.Step
	direct := len(p.Steps) == 0 && mayAnswer
	if direct {
		// No tool is needed: the reply is written from the model's own knowledge
		// and labelled so. There is nothing to verify against, so no criteria.
		p.CompletionCriteria = nil
	} else if steps, err = e.validatePlan(t, p, 1, nil); err != nil {
		return err
	}
	if complexPlan(p) {
		if rp, rsteps, ok := e.review(ctx, t, p); ok {
			p, steps = rp, rsteps
		}
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		cur, err := task.Get(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if cur.State != task.Planning {
			return errStop // paused or cancelled meanwhile
		}
		if err := task.InsertSteps(ctx, tx, t.ID, steps); err != nil {
			return err
		}
		ck, err := putCheckpoint(ctx, tx, t.ID, "", "plan", p)
		if err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, t.ID, nil, "PLAN_CREATED", "worker", map[string]any{
			"steps": len(steps), "checkpoint": ck, "completion_criteria": p.CompletionCriteria, "direct_answer": direct, "asked_in_reply": inReply}); err != nil {
			return err
		}
		_, err = task.Transition(ctx, tx, t.ID, task.Running, "", "", "core")
		return err
	})
}

// askOwner records a clarification question and parks the task in WAITING_INPUT.
func (e *Engine) askOwner(ctx context.Context, t *task.Task, stepID, question string) error {
	q := truncate(strings.TrimSpace(question), 1000)
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		id := ids.New()
		var sid any
		if stepID != "" {
			sid = stepID
		}
		if _, err := tx.ExecContext(ctx, "insert into task_inputs(id, task_id, step_id, question, status, created_at) values(?,?,?,?,?,?)",
			id, t.ID, sid, q, "pending", ids.NowMs()); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, t.ID, ptr(stepID), "INPUT_REQUESTED", "worker", map[string]any{"input_id": id, "question": q}); err != nil {
			return err
		}
		if _, err := task.Transition(ctx, tx, t.ID, task.WaitingInput, "", "input:"+id, "core"); err != nil {
			return err
		}
		return store.Commit(errStop) // commit, then stop driving the task
	})
}

// complexPlan is the plan-review rule (ADR-0016).
func complexPlan(p *planDoc) bool {
	if len(p.Steps) >= 6 {
		return true
	}
	for _, s := range p.Steps {
		if s.ToolID == nil {
			continue
		}
		if *s.ToolID == "agent.delegate" {
			return true
		}
		if spec, ok := policy.Lookup(*s.ToolID); ok && spec.Tier(planArgs(&s)) == policy.A2 {
			return true
		}
	}
	return false
}

// planArgs exposes planned arguments to manifest tier rules.
func planArgs(s *plannedStep) map[string]any {
	m := map[string]any{}
	if s.Path != nil {
		m["path"] = *s.Path
	}
	if s.URL != nil {
		m["url"] = *s.URL
	}
	return m
}

// review asks for a critique of a complex plan; a valid revised plan replaces it.
func (e *Engine) review(ctx context.Context, t *task.Task, p *planDoc) (*planDoc, []task.Step, bool) {
	s, err := e.newSession(ctx, t)
	if err != nil {
		return nil, nil, false
	}
	raw, err := e.callWorker(ctx, s, "task.review", map[string]any{"objective": t.Objective, "plan": p,
		"granted_tools": t.Scope.Tools, "tier": tierFor(t)})
	var r struct {
		OK      bool     `json:"ok"`
		Issues  []string `json:"issues"`
		Revised *planDoc `json:"revised"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	record := func(data map[string]any) {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, t.ID, nil, "PLAN_REVIEWED", "worker", data)
			return err
		})
	}
	if err != nil {
		record(map[string]any{"ok": false, "error": reasonCode(err), "used": "original"})
		return nil, nil, false
	}
	if r.OK || r.Revised == nil {
		record(map[string]any{"ok": r.OK, "issues": r.Issues, "used": "original"})
		return nil, nil, false
	}
	if len(r.Revised.CompletionCriteria) == 0 {
		r.Revised.CompletionCriteria = p.CompletionCriteria
	}
	steps, verr := e.validatePlan(t, r.Revised, 1, nil)
	if verr != nil {
		record(map[string]any{"ok": false, "issues": r.Issues, "used": "original", "revised_invalid": verr.Error()})
		return nil, nil, false
	}
	record(map[string]any{"ok": false, "issues": r.Issues, "used": "revised", "steps": len(steps)})
	return r.Revised, steps, true
}

// validatePlan re-checks a plan in Core: numbering from startNo, granted tools,
// required arguments, objective-named outputs (counting files already written,
// alreadyWritten). Side-effect class comes from the trusted manifest.
func (e *Engine) validatePlan(t *task.Task, p *planDoc, startNo int, alreadyWritten map[string]bool) ([]task.Step, error) {
	if len(p.Steps) == 0 || len(p.Steps) > maxPlanSteps {
		return nil, fmt.Errorf("invalid_plan: %d steps", len(p.Steps))
	}
	allowed := policy.Scopes{Tools: t.Scope.Tools}
	var out []task.Step
	for i := range p.Steps {
		ps := &p.Steps[i]
		if ps.StepNo != startNo+i {
			return nil, fmt.Errorf("invalid_plan: steps must be numbered %d..%d", startNo, startNo+len(p.Steps)-1)
		}
		if ps.ToolID == nil {
			return nil, fmt.Errorf("invalid_plan: step %d has no tool", ps.StepNo)
		}
		spec, ok := policy.Lookup(*ps.ToolID)
		if !ok || !allowed.HasTool(spec.ID) {
			return nil, fmt.Errorf("invalid_plan: step %d tool %q not granted", ps.StepNo, *ps.ToolID)
		}
		if err := requiredArgs(ps); err != nil {
			return nil, fmt.Errorf("invalid_plan: step %d: %v", ps.StepNo, err)
		}
		if len(ps.Description) > 500 {
			ps.Description = ps.Description[:500]
		}
		tool := spec.ID
		b, _ := json.Marshal(ps)
		pj := string(b)
		out = append(out, task.Step{StepNo: ps.StepNo, Description: ps.Description, ToolID: &tool, SideEffect: spec.SideEffect, PlanJSON: &pj})
	}
	if allowed.HasTool("workspace.write") {
		written := map[string]bool{}
		for k := range alreadyWritten {
			written[k] = true
		}
		for _, ps := range p.Steps {
			if ps.ToolID != nil && *ps.ToolID == "workspace.write" && ps.Path != nil {
				written[*ps.Path] = true
			}
		}
		inputs := inputPaths(t, p.Steps)
		for _, want := range ExpectedOutputs(t.Objective, inputs) {
			if !written[want] && !inputs[want] {
				return nil, fmt.Errorf("invalid_plan: the plan never writes %s, which the objective asks for", want)
			}
		}
	}
	if want := RequiredDelegations(t.Objective); want > 0 && allowed.HasTool("agent.delegate") {
		var done int // subagents that already finished in this task (a replan keeps them)
		e.st.R.QueryRow("select count(*) from task_steps where task_id=? and tool_id='agent.delegate' and state='SUCCEEDED'", t.ID).Scan(&done)
		have := done
		for _, ps := range p.Steps {
			if ps.ToolID != nil && *ps.ToolID == "agent.delegate" {
				have++
			}
		}
		if have < want {
			return nil, fmt.Errorf("invalid_plan: the objective asks for %d subagent(s) (agent.delegate); the plan has %d", want, have)
		}
	}
	return out, nil
}

// inputPaths are files the task reads rather than produces: attachments and
// paths of read steps that no step writes.
func inputPaths(t *task.Task, steps []plannedStep) map[string]bool {
	in := map[string]bool{}
	for _, a := range t.Scope.Attachments {
		in[a] = true
	}
	writes := map[string]bool{}
	for _, s := range steps {
		if s.ToolID != nil && s.Path != nil && *s.ToolID == "workspace.write" {
			writes[*s.Path] = true
		}
	}
	for _, s := range steps {
		if s.ToolID != nil && s.Path != nil && (*s.ToolID == "workspace.read" || *s.ToolID == "data.read") && !writes[*s.Path] {
			in[*s.Path] = true
		}
	}
	return in
}

// requiredArgs checks that each tool's planning-time arguments are present.
// web.fetch may leave url null: it is then chosen at step time and must have
// provenance (ADR-0017).
func requiredArgs(ps *plannedStep) error {
	has := func(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }
	switch *ps.ToolID {
	case "workspace.read", "workspace.write", "data.read":
		if !has(ps.Path) {
			return errors.New("path is required")
		}
	case "web.search":
		if !has(ps.Query) {
			return errors.New("query is required")
		}
	case "calc":
		// expression may be null: built at step time from earlier results (calc is pure)
	case "wait.event":
		if !has(ps.Key) {
			return errors.New("key is required")
		}
	case "wait.until":
		if !has(ps.Until) {
			return errors.New("until is required")
		}
	case "agent.delegate":
		if !has(ps.Brief) {
			return errors.New("brief is required")
		}
	default:
		if *ps.ToolID == "browser.type" || *ps.ToolID == "browser.select" {
			field := map[string]string{"browser.type": "text", "browser.select": "value"}[*ps.ToolID]
			var m map[string]any
			if !has(ps.Input) || json.Unmarshal([]byte(*ps.Input), &m) != nil {
				return fmt.Errorf("input must be a JSON object with %q", field)
			}
			if _, ok := m[field].(string); !ok {
				return fmt.Errorf("input must set %q", field)
			}
			return nil
		}
		if *ps.ToolID == "browser.upload" && !has(ps.Path) {
			return errors.New("browser.upload needs path")
		}
		if *ps.ToolID == "browser.login" {
			var m map[string]any
			if !has(ps.Input) || json.Unmarshal([]byte(*ps.Input), &m) != nil {
				return errors.New(`input must be {"profile": "..."}`)
			}
			if _, ok := m["profile"].(string); !ok {
				return errors.New(`input must set "profile"`)
			}
		}
		if strings.HasPrefix(*ps.ToolID, "browser.") {
			return nil
		}
		if strings.HasPrefix(*ps.ToolID, "mcp.") || strings.HasPrefix(*ps.ToolID, "l2.") || strings.HasPrefix(*ps.ToolID, "connector.") {
			var m map[string]any
			if !has(ps.Input) || json.Unmarshal([]byte(*ps.Input), &m) != nil {
				return errors.New("input must be a JSON object with the tool's arguments")
			}
		}
	}
	return nil
}

// stepPlan returns a step's planned arguments.
func stepPlan(st *task.Step) (*plannedStep, error) {
	if st.PlanJSON == nil {
		return nil, fmt.Errorf("step %d has no planned arguments", st.StepNo)
	}
	var ps plannedStep
	return &ps, json.Unmarshal([]byte(*st.PlanJSON), &ps)
}

// legacyPlanStep reads a step's arguments from a P1 plan checkpoint.
func (e *Engine) legacyPlanStep(ctx context.Context, st *task.Step) (*plannedStep, error) {
	var raw string
	if err := e.st.R.QueryRowContext(ctx, "select state_json from checkpoints where task_id=? and label='plan' order by created_at limit 1", st.TaskID).Scan(&raw); err != nil {
		return nil, fmt.Errorf("plan checkpoint: %w", err)
	}
	var p planDoc
	if err := json.Unmarshal([]byte(raw), &p); err != nil || st.StepNo > len(p.Steps) {
		return nil, fmt.Errorf("plan checkpoint unreadable")
	}
	return &p.Steps[st.StepNo-1], nil
}

func (e *Engine) criteria(ctx context.Context, taskID string) []string {
	var raw string
	if err := e.st.R.QueryRowContext(ctx, "select state_json from checkpoints where task_id=? and label='plan' order by created_at desc limit 1", taskID).Scan(&raw); err != nil {
		return nil
	}
	var p planDoc
	json.Unmarshal([]byte(raw), &p)
	return p.CompletionCriteria
}

// replannable failures can be handled by a new plan for the remaining work.
func replannable(err error) bool {
	// A failure the worker reports arrives as an RPC error whose reason code says
	// what failed; its message starts with the RPC code, not the reason.
	switch reasonCode(err) {
	case "step_failed", "invalid_step_result", "tool_failed":
		return true
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "step_failed") || strings.HasPrefix(msg, "invalid_step_result") ||
		strings.HasPrefix(msg, "tool_failed")
}

// execute runs remaining steps, replanning after a failed step, then finishes.
func (e *Engine) execute(ctx context.Context, t *task.Task) error {
	for {
		steps, err := task.Steps(ctx, e.st.R, t.ID)
		if err != nil {
			return err
		}
		// Steps whose subagent is still running (parallel fan-out, ADR-0047).
		open := map[string]bool{}
		if rows, err := e.st.R.QueryContext(ctx, "select step_id from task_waits where task_id=? and kind='tasks' and satisfied_at is null and step_id is not null", t.ID); err == nil {
			for rows.Next() {
				var id string
				rows.Scan(&id)
				open[id] = true
			}
			rows.Close()
		}
		var next *task.Step
		blocked := false
		for _, st := range steps {
			if st.State == task.StepSucceeded || st.State == task.StepSkipped {
				continue
			}
			if open[st.ID] {
				blocked = true // a child is running for this step
				continue
			}
			if blocked && !(st.ToolID != nil && *st.ToolID == "agent.delegate" && st.State == task.StepPending) {
				next = nil // this step needs the running children's results first
				break
			}
			next = st
			break
		}
		if next == nil && len(open) > 0 {
			// Park until a child finishes; each finished child wakes the parent once.
			if err := e.st.Write(ctx, func(tx *sql.Tx) error {
				_, err := task.Transition(ctx, tx, t.ID, task.Waiting, "", fmt.Sprintf("subagents:%d", len(open)), "core")
				return err
			}); err != nil {
				return err
			}
			return errStop
		}
		if next == nil {
			done, err := e.finalize(ctx, t.ID)
			if done || err != nil {
				return err
			}
			continue // replanned after a failed check or verification
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		cur, err := task.Get(ctx, e.st.R, t.ID)
		if err != nil {
			return err
		}
		if cur.State != task.Running {
			return errStop
		}
		ps, err := stepPlan(next)
		if err != nil {
			if ps, err = e.legacyPlanStep(ctx, next); err != nil {
				return err
			}
		}
		var stepErr error
		if next.State == task.StepFailed {
			// Failed outside a worker call (e.g. a delegated subagent failed).
			stepErr = fmt.Errorf("step_failed: step %d: %s", next.StepNo, deref(next.Reason))
		} else {
			stepErr = e.runStep(ctx, cur, next, ps)
		}
		if err := stepErr; err != nil {
			if strings.Contains(err.Error(), "site_blocked") {
				// Blueprint 8.8.1 rule 6a: stop and record SITE_BLOCKED; no other route around the block.
				return fmt.Errorf("site_blocked: %v", err)
			}
			if errors.Is(err, errStop) || !replannable(err) || cur.Replans >= maxReplans {
				return err
			}
			if rerr := e.replan(ctx, cur, map[string]any{"kind": "step_failed", "step_no": next.StepNo,
				"reason": truncate(err.Error(), 500)}); rerr != nil {
				return rerr
			}
		}
	}
}

// replan asks the worker for replacement steps for the remaining work.
func (e *Engine) replan(ctx context.Context, t *task.Task, cause map[string]any) error {
	cur, err := task.Get(ctx, e.st.R, t.ID)
	if err != nil {
		return err
	}
	if cur.Replans >= maxReplans {
		return fmt.Errorf("replan_limit: %v", cause["kind"])
	}
	steps, err := task.Steps(ctx, e.st.R, t.ID)
	if err != nil {
		return err
	}
	var summary []map[string]any
	written := map[string]bool{}
	next := 1
	for _, st := range steps {
		item := map[string]any{"step_no": st.StepNo, "description": st.Description, "tool_id": st.ToolID, "state": st.State}
		if st.Reason != nil {
			item["reason"] = *st.Reason
		}
		summary = append(summary, item)
		if st.StepNo >= next {
			next = st.StepNo + 1
		}
		if st.State == task.StepSucceeded && st.ToolID != nil && *st.ToolID == "workspace.write" {
			if ps, err := stepPlan(st); err == nil && ps.Path != nil {
				written[*ps.Path] = true
			}
		}
	}
	tier := tierFor(cur)
	if tier == "" && cur.Replans >= 1 && cause["kind"] == "verification_failed" {
		tier = "deep" // cascade: repeated verification failure escalates (ADR-0016)
	}
	s, err := e.newSession(ctx, cur)
	if err != nil {
		return err
	}
	octx, _ := e.ownerContext(ctx, cur)
	obs, _ := e.observations(ctx, cur, next)
	guidance, _ := e.brainGuidance(ctx, cur)
	raw, err := e.callWorker(ctx, s, "task.replan", map[string]any{"guidance": guidance, "objective": cur.Objective, "granted_tools": cur.Scope.Tools,
		"allow_domains": cur.Scope.AllowDomains, "steps": summary, "cause": cause, "next_step_no": next,
		"completion_criteria": e.criteria(ctx, t.ID), "observations": obs, "owner_context": octx, "tier": tier,
		"attachments": cur.Scope.Attachments, "tool_docs": e.toolDocs(cur)})
	if s.fatal != nil {
		return s.fatal
	}
	if err != nil {
		return err
	}
	p, err := decodePlan(raw)
	if err != nil {
		return err
	}
	newSteps, err := e.validatePlan(cur, p, next, written)
	if err != nil {
		return err
	}
	if len(p.CompletionCriteria) == 0 {
		p.CompletionCriteria = e.criteria(ctx, t.ID)
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		for _, st := range steps {
			if st.State != task.StepSucceeded && st.State != task.StepSkipped {
				reason := "superseded_by_replan"
				if st.Reason != nil {
					reason += ": " + *st.Reason
				}
				if err := task.UpdateStep(ctx, tx, st, task.StepSkipped, "", "", truncate(reason, 300), "STEP_SKIPPED", nil); err != nil {
					return err
				}
			}
		}
		if err := task.InsertSteps(ctx, tx, t.ID, newSteps); err != nil {
			return err
		}
		if _, err := putCheckpoint(ctx, tx, t.ID, "", "plan", p); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update tasks set replans = replans + 1 where id=?", t.ID); err != nil {
			return err
		}
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, "REPLANNED", "worker", map[string]any{
			"cause": cause, "new_steps": len(newSteps), "from_step": next, "replan": cur.Replans + 1, "tier": tier})
		return err
	})
}

// finalize runs the deterministic checks and verification. It returns done=true
// when the task reached a final state (or waits), false after a replan.
func (e *Engine) finalize(ctx context.Context, taskID string) (bool, error) {
	t, err := task.Get(ctx, e.st.R, taskID)
	if err != nil {
		return true, err
	}
	problems, evidence, err := e.deterministicChecks(ctx, t)
	if err != nil {
		return true, err
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, taskID, nil, "COMPLETION_CHECK", "core", map[string]any{"ok": len(problems) == 0, "problems": problems})
		return err
	})
	if len(problems) > 0 {
		return e.retryOrFail(ctx, t, "completion_check_failed", map[string]any{"kind": "completion_check_failed", "problems": problems})
	}
	criteria := e.criteria(ctx, taskID)
	if len(criteria) == 0 {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, taskID, nil, "VERIFICATION_SKIPPED", "core", map[string]any{"reason": "plan has no completion criteria; deterministic checks only"})
			return err
		})
	} else {
		failures, err := e.verify(ctx, t, criteria, evidence)
		if err != nil {
			return true, err
		}
		if len(failures) > 0 {
			return e.retryOrFail(ctx, t, "verification_failed", map[string]any{"kind": "verification_failed", "failures": failures})
		}
	}
	if e.wantsReply(t) {
		// The owner is told the result before the task counts as done (ADR-0055).
		if err := e.reply(ctx, t, evidence); err != nil {
			return true, err
		}
	}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := task.Transition(ctx, tx, taskID, task.Succeeded, "", "", "core"); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, taskID)
	})
	if err == nil && len(criteria) > 0 {
		e.reflect(ctx, taskID, evidence) // learning only from verified successes (ADR-0025)
	}
	return true, err
}

func (e *Engine) retryOrFail(ctx context.Context, t *task.Task, reason string, cause map[string]any) (bool, error) {
	if t.Replans < maxReplans {
		err := e.replan(ctx, t, cause)
		if err == nil {
			return false, nil
		}
		if errors.Is(err, errStop) || ctx.Err() != nil || isFatal(err) {
			return true, err
		}
		// Record why the replan was rejected, so the failure can be diagnosed.
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, t.ID, nil, "REPLAN_FAILED", "core", map[string]any{"error": truncate(err.Error(), 500), "cause": cause["kind"]})
			return err
		})
	}
	if ctx.Err() == nil {
		detail, _ := json.Marshal(cause)
		e.failureReply(ctx, t, reason, string(detail))
	}
	return true, e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := task.Transition(ctx, tx, t.ID, task.Failed, reason, "", "core"); err != nil {
			return err
		}
		if err := policy.RevokeTask(ctx, tx, t.ID); err != nil {
			return err
		}
		return store.Commit(errStop) // commit, then stop driving the task
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// isFatal errors end the task instead of triggering a replan.
func isFatal(err error) bool {
	return errors.Is(err, budget.ErrTaskBudget) || errors.Is(err, budget.ErrDailyBudget) || errors.Is(err, budget.ErrIterations) ||
		errors.Is(err, budget.ErrHalted) || errors.Is(err, gateway.ErrInvalidOutput) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

// evidenceItem is produced output shown to the verifier and searched for quotes.
type evidenceItem struct {
	Source  string `json:"source"`
	Kind    string `json:"kind"` // file | observation
	Content string `json:"content"`
}

// deterministicChecks verifies, without any model, that objective-named outputs
// exist and every recorded write is present with its recorded hash. It also
// collects the evidence for verification.
func (e *Engine) deterministicChecks(ctx context.Context, t *task.Task) ([]string, []evidenceItem, error) {
	steps, err := task.Steps(ctx, e.st.R, t.ID)
	if err != nil {
		return nil, nil, err
	}
	var problems []string
	evidence := []evidenceItem{}
	var planned []plannedStep
	for _, s := range steps {
		if ps, err := stepPlan(s); err == nil {
			planned = append(planned, *ps)
		}
	}
	if (policy.Scopes{Tools: t.Scope.Tools}).HasTool("workspace.write") {
		for _, want := range ExpectedOutputs(t.Objective, inputPaths(t, planned)) {
			if h, err := e.ws.FileHash(t.ID, want); err != nil || h == "" {
				problems = append(problems, fmt.Sprintf("%s named in the objective was not produced", want))
			}
		}
	}
	// The latest write of each path is the one that must be on disk; an earlier
	// write of the same path was legitimately replaced (e.g. after a replan).
	latest := map[string]writeIntent{}
	var order []string
	for _, s := range steps {
		switch s.State {
		case task.StepSkipped:
			continue
		case task.StepSucceeded:
		default:
			problems = append(problems, fmt.Sprintf("step %d is %s", s.StepNo, s.State))
			continue
		}
		if s.ToolID != nil && *s.ToolID == "workspace.write" {
			var in writeIntent
			if err := e.checkpointJSON(ctx, s.OutcomeRef, &in); err != nil {
				problems = append(problems, fmt.Sprintf("step %d: no recorded write", s.StepNo))
				continue
			}
			if _, seen := latest[in.Path]; !seen {
				order = append(order, in.Path)
			}
			latest[in.Path] = in
		}
	}
	for _, path := range order {
		in := latest[path]
		h, err := e.ws.FileHash(t.ID, in.Path)
		if err != nil || h != in.SHA256 {
			problems = append(problems, fmt.Sprintf("%s missing or changed after it was written", in.Path))
			continue
		}
		evidence = append(evidence, evidenceItem{Source: in.Path, Kind: "file", Content: truncate(in.Content, 20000)})
	}
	obs, err := e.observations(ctx, t, 1<<30)
	if err != nil {
		return nil, nil, err
	}
	for _, o := range obs {
		if o.ToolID != nil && *o.ToolID == "workspace.write" {
			continue
		}
		evidence = append(evidence, evidenceItem{Source: o.Source, Kind: "observation", Content: truncate(o.Content, 8000)})
	}
	return problems, evidence, nil
}

type criterionResult struct {
	Criterion     string `json:"criterion"`
	Met           bool   `json:"met"`
	EvidenceQuote string `json:"evidence_quote"`
}

// verify asks the worker to check each completion criterion against the
// evidence. Model confidence is not evidence: a criterion counts as met only
// if its quote is found in the evidence by the Core (ADR-0016).
func (e *Engine) verify(ctx context.Context, t *task.Task, criteria []string, evidence []evidenceItem) ([]string, error) {
	// "<file> exists" is decided by the Core from the files it holds; only the
	// other criteria need a model (and none at all if every one is about existence).
	decided := map[string]criterionResult{}
	var modelCriteria []string
	for _, c := range criteria {
		if path, ok := existenceCriterion(c); ok {
			res := criterionResult{Criterion: c}
			for _, ev := range evidence {
				if ev.Kind == "file" && ev.Source == path {
					res.Met, res.EvidenceQuote = true, "core: "+path+" was written by this task"
				}
			}
			decided[c] = res
			continue
		}
		modelCriteria = append(modelCriteria, c)
	}
	var r struct {
		Criteria []criterionResult `json:"criteria"`
	}
	if len(modelCriteria) > 0 {
		s, err := e.newSession(ctx, t)
		if err != nil {
			return nil, err
		}
		guidance, _ := e.brainGuidance(ctx, t)
		raw, err := e.callWorker(ctx, s, "task.verify", map[string]any{"guidance": guidance, "objective": t.Objective, "completion_criteria": modelCriteria,
			"evidence": evidence, "tier": tierFor(t)})
		if s.fatal != nil {
			return nil, s.fatal
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("invalid_verification: %v", err)
		}
	}
	byCriterion := map[string]criterionResult{}
	for _, c := range r.Criteria {
		byCriterion[c.Criterion] = c
	}
	var failures []string
	var checked []map[string]any
	mi := 0 // position among the criteria sent to the model
	for _, c := range criteria {
		if res, ok := decided[c]; ok {
			if !res.Met {
				failures = append(failures, fmt.Sprintf("%q: not met (the Core holds no such file)", c))
			}
			checked = append(checked, map[string]any{"criterion": c, "met": res.Met, "evidence_found": res.Met, "quote": res.EvidenceQuote, "decided_by": "core"})
			continue
		}
		i := mi
		mi++
		res, ok := byCriterion[c]
		if !ok && i < len(r.Criteria) {
			res, ok = r.Criteria[i], true
		}
		found := ok && res.Met && quoteFound(res.EvidenceQuote, evidence)
		switch {
		case !ok:
			failures = append(failures, fmt.Sprintf("%q: not assessed", c))
		case !res.Met:
			failures = append(failures, fmt.Sprintf("%q: not met", c))
		case !found:
			failures = append(failures, fmt.Sprintf("%q: claimed met but the quoted evidence is not in the output", c))
		}
		checked = append(checked, map[string]any{"criterion": c, "met": ok && res.Met, "evidence_found": found,
			"quote": truncate(res.EvidenceQuote, 200)})
	}
	typ := "VERIFIED"
	if len(failures) > 0 {
		typ = "VERIFICATION_FAILED"
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, typ, "core", map[string]any{"criteria": checked, "failures": failures})
		return err
	})
	return failures, nil
}

var existenceRe = regexp.MustCompile(`(?i)^\s*(?:the\s+)?(?:file\s+|output\s+)?` + "`?" + `((?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_-]+\.[A-Za-z0-9]{1,5})` + "`?" + `\s+(?:file\s+)?(?:exists|is\s+created|has\s+been\s+(?:written|created))\.?\s*$`)

// existenceCriterion recognizes a criterion that only says a file exists
// ("reports/a.md exists", "The file reports/a.md has been created.").
func existenceCriterion(c string) (string, bool) {
	m := existenceRe.FindStringSubmatch(c)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func normalizeText(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

// quoteFound reports whether a quote appears in the evidence: verbatim after
// normalizing case, punctuation and whitespace, or with at least 80 % of its
// words appearing in order in one evidence item (tolerates small formatting).
func quoteFound(quote string, evidence []evidenceItem) bool {
	q := normalizeText(quote)
	if len(q) == 0 {
		return false
	}
	if len(q) < 2 {
		// One word proves nothing, unless it is the whole of an evidence item
		// (a file containing only "1024").
		for _, ev := range evidence {
			if w := normalizeText(ev.Content); len(w) == 1 && w[0] == q[0] {
				return true
			}
		}
		return false
	}
	qs := " " + strings.Join(q, " ") + " "
	for _, ev := range evidence {
		words := normalizeText(ev.Content)
		if strings.Contains(" "+strings.Join(words, " ")+" ", qs) {
			return true
		}
		// in-order subsequence match
		j, hit := 0, 0
		for _, w := range words {
			if j < len(q) && w == q[j] {
				j++
				hit++
			}
		}
		if len(q) >= 5 && float64(hit) >= 0.8*float64(len(q)) {
			return true
		}
	}
	return false
}
