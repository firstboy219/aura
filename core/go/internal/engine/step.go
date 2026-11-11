package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/rpc"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/worker"
)

const maxObservationChars = 50_000

// maxStepRecoveries caps how often one step may be resumed after a Core crash.
const maxStepRecoveries = 10

// runStep executes one step: replay a recorded write intent, execute an
// approved action, or ask the worker to perform the step.
func (e *Engine) runStep(ctx context.Context, t *task.Task, st *task.Step, ps *plannedStep) error {
	if st.State == task.StepWaitingApproval {
		ap, err := policy.ApprovedFor(ctx, e.st.R, st.ID)
		if err != nil {
			return err
		}
		if ap != nil {
			return e.executeApproved(ctx, t, st, ap)
		}
	}
	if st.State == task.StepStarted && st.SideEffect == "write_idempotent" && st.OutcomeRef != nil {
		var in writeIntent
		if err := e.checkpointJSON(ctx, st.OutcomeRef, &in); err == nil && in.Path != "" {
			return e.replayIntent(ctx, t, st, in)
		}
	}
	var recoveries int
	e.st.R.QueryRowContext(ctx, "select count(*) from task_events where step_id=? and type='STEP_RECOVERED'", st.ID).Scan(&recoveries)
	if recoveries >= maxStepRecoveries {
		e.failStep(ctx, st, "recovery_limit")
		return fmt.Errorf("step_failed: step %d was interrupted by %d Core restarts", st.StepNo, recoveries)
	}
	for {
		if st.Attempts >= maxStepAttempts {
			e.failStep(ctx, st, "max_attempts")
			return fmt.Errorf("step_failed: step %d exceeded %d attempts", st.StepNo, maxStepAttempts)
		}
		if err := e.st.Write(ctx, func(tx *sql.Tx) error {
			return task.UpdateStep(ctx, tx, st, task.StepStarted, "", "", "", "STEP_STARTED", map[string]any{"tool_id": st.ToolID})
		}); err != nil {
			return err
		}
		err := e.workerStep(ctx, t, st, ps)
		if err == nil || errors.Is(err, errStop) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) {
			e.failStep(ctx, st, reasonCode(err))
			return err
		}
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err2 := task.AppendEvent(ctx, tx, t.ID, &st.ID, "STEP_ATTEMPT_FAILED", "core", map[string]any{
				"step_no": st.StepNo, "attempt": st.Attempts, "reason": reasonCode(err)})
			return err2
		})
		// A recorded write intent is replayed rather than regenerated.
		fresh, gerr := task.GetStep(ctx, e.st.R, st.ID)
		if gerr == nil && fresh.OutcomeRef != nil && st.SideEffect == "write_idempotent" {
			var in writeIntent
			if e.checkpointJSON(ctx, fresh.OutcomeRef, &in) == nil && in.Path != "" {
				return e.replayIntent(ctx, t, fresh, in)
			}
		}
	}
}

// retryable: worker crashes, protocol errors and timeouts are retried; policy,
// budget, invalid output and explicit failures are not.
func retryable(err error) bool {
	return errors.Is(err, worker.ErrCrashed) || errors.Is(err, worker.ErrProtocol) || strings.HasPrefix(err.Error(), "worker_timeout")
}

func (e *Engine) failStep(ctx context.Context, st *task.Step, reason string) {
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		return task.UpdateStep(ctx, tx, st, task.StepFailed, "", "", reason, "STEP_FAILED", nil)
	})
}

type stepResult struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (e *Engine) workerStep(ctx context.Context, t *task.Task, st *task.Step, ps *plannedStep) error {
	s, err := e.newSession(ctx, t)
	if err != nil {
		return err
	}
	s.step, s.plan = st, ps
	obs, err := e.observations(ctx, t, st.StepNo)
	if err != nil {
		return err
	}
	octx, err := e.ownerContext(ctx, t)
	if err != nil {
		return err
	}
	raw, err := e.callWorker(ctx, s, "task.step", map[string]any{"objective": e.objectiveFor(ctx, t), "step": ps, "observations": obs,
		"owner_context": octx, "tier": tierFor(t), "profile": e.profileContext(ctx, t)})
	if s.fatal != nil {
		return s.fatal
	}
	if err != nil {
		return err
	}
	var r stepResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("invalid_step_result: %v", err)
	}
	switch r.Decision {
	case "step_done":
		if !s.executed {
			return errors.New("invalid_step_result: step_done without a successful tool call")
		}
		return e.st.Write(ctx, func(tx *sql.Tx) error {
			return task.UpdateStep(ctx, tx, st, task.StepSucceeded, "", "", "", "STEP_SUCCEEDED", nil)
		})
	case "waiting_approval":
		if s.approvalID == "" {
			return errors.New("invalid_step_result: waiting_approval without an approval request")
		}
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.Transition(ctx, tx, t.ID, task.WaitingApproval, "", "approval:"+s.approvalID, "core")
			return err
		})
		if err != nil {
			return err
		}
		return errStop
	case "waiting":
		if s.waitID == "" {
			return errors.New("invalid_step_result: waiting without a supervisor wait")
		}
		if cur, err := task.Get(ctx, e.st.R, t.ID); err == nil && cur.State == task.Running && st.ToolID != nil && *st.ToolID == "agent.delegate" {
			return nil // parallel fan-out: the child runs; the execute loop continues with the next step
		}
		return errStop // the task is parked in WAITING; the supervisor resumes it
	case "fail":
		if s.denied {
			// A policy denial is not a recoverable step failure: never replan around it.
			return fmt.Errorf("capability_denied: step %d attempted an action outside its grant or policy", st.StepNo)
		}
		return fmt.Errorf("step_failed: %s", truncate(r.Reason, 200))
	}
	return fmt.Errorf("invalid_step_result: decision %q", r.Decision)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type observation struct {
	StepNo  int     `json:"step_no"`
	ToolID  *string `json:"tool_id"`
	Source  string  `json:"source"`
	Trust   string  `json:"trust"`
	Content string  `json:"content"`
}

type obsRecord struct {
	Source  string `json:"source"`
	Content string `json:"content"`
}

// observations are earlier steps' outputs, labelled untrusted.
func (e *Engine) observations(ctx context.Context, t *task.Task, before int) ([]observation, error) {
	steps, err := task.Steps(ctx, e.st.R, t.ID)
	if err != nil {
		return nil, err
	}
	out := []observation{}
	for _, s := range steps {
		if s.StepNo >= before || s.State != task.StepSucceeded || s.ToolID == nil {
			continue
		}
		o := observation{StepNo: s.StepNo, ToolID: s.ToolID, Trust: "untrusted"}
		switch *s.ToolID {
		case "workspace.write":
			var in writeIntent
			if e.checkpointJSON(ctx, s.OutcomeRef, &in) == nil {
				o.Source, o.Content = in.Path, fmt.Sprintf("wrote %s (sha256 %s)", in.Path, in.SHA256)
			}
		default:
			var r obsRecord
			if e.checkpointJSON(ctx, s.OutcomeRef, &r) == nil {
				o.Source, o.Content = r.Source, r.Content
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// serve handles one worker→Core request. Every request is authorized against
// the Core grant registry; denials are audited.
func (e *Engine) serve(ctx context.Context, s *session, req *rpc.Envelope) (any, *rpc.Error) {
	if req.Meta == nil || req.Meta.TaskID != s.t.ID {
		return nil, e.deny(ctx, s, req.Method, "task_mismatch", nil)
	}
	g, err := policy.Verify(ctx, e.st.R, req.Meta.CapabilityToken, s.t.ID, s.gen)
	if err != nil {
		return nil, e.deny(ctx, s, req.Method, "capability_denied", nil)
	}
	switch req.Method {
	case "llm.complete":
		return e.serveLLM(ctx, s, req)
	case "tool.invoke":
		return e.serveTool(ctx, s, g, req, false)
	case "approval.request":
		return e.serveTool(ctx, s, g, req, true)
	case "task.checkpoint":
		var p struct {
			State json.RawMessage `json:"state"`
			Label string          `json:"label"`
		}
		if err := strictDecode(req.Params, &p); err != nil || len(p.State) == 0 || len(p.Label) > 100 {
			return nil, rpc.Errf(rpc.CodeInvalidParams, "invalid_params", "task.checkpoint: invalid params")
		}
		var id string
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			var err error
			id, err = putCheckpoint(ctx, tx, s.t.ID, stepID(s), "worker:"+p.Label, p.State)
			return err
		})
		if err != nil {
			return nil, rpc.Errf(rpc.CodeInternal, "internal", "checkpoint failed")
		}
		return map[string]string{"checkpoint_id": id}, nil
	}
	return nil, e.deny(ctx, s, req.Method, "method_not_allowed", nil)
}

func stepID(s *session) string {
	if s.step == nil {
		return ""
	}
	return s.step.ID
}

func strictDecode(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (e *Engine) deny(ctx context.Context, s *session, method, reason string, extra map[string]any) *rpc.Error {
	s.denied = true
	data := map[string]any{"method": method, "reason": reason, "worker_generation": s.gen}
	for k, v := range extra {
		data[k] = v
	}
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		if err := policy.Audit(ctx, tx, "CAPABILITY_DENIED", "worker", s.t.ID, "", data); err != nil {
			return err
		}
		_, err := task.AppendEvent(ctx, tx, s.t.ID, nil, "CAPABILITY_DENIED", "core", data)
		return err
	})
	e.log.Warn("worker request denied", "task", s.t.ID, "method", method, "reason", reason)
	return rpc.Errf(rpc.CodeDenied, reason, "denied: %s", reason)
}

const maxPromptChars = 200_000

func (e *Engine) serveLLM(ctx context.Context, s *session, req *rpc.Envelope) (any, *rpc.Error) {
	var p gateway.Request
	if err := strictDecode(req.Params, &p); err != nil || len(p.Messages) == 0 || len(p.Messages) > 50 {
		return nil, rpc.Errf(rpc.CodeInvalidParams, "invalid_params", "llm.complete: invalid params")
	}
	size := len(p.ResponseSchema)
	for _, m := range p.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			return nil, rpc.Errf(rpc.CodeInvalidParams, "invalid_params", "llm.complete: bad role")
		}
		size += len(m.Content)
	}
	if size > maxPromptChars {
		return nil, rpc.Errf(rpc.CodeInvalidParams, "prompt_too_large", "llm.complete: prompt too large")
	}
	p.DataClass = "private"
	if s.t.Scope.IsPublic() {
		p.DataClass = "public"
	}
	rootLimit := s.t.Budget.MaxCostUSD
	if s.t.RootTaskID != s.t.ID {
		if root, err := task.Get(ctx, e.st.R, s.t.RootTaskID); err == nil {
			rootLimit = root.Budget.MaxCostUSD
		}
	}
	res, err := e.gw.Complete(ctx, gateway.Budget{TaskID: s.t.ID, StepID: stepID(s), TaskLimitUSD: s.t.Budget.MaxCostUSD,
		RootTaskID: s.t.RootTaskID, RootLimitUSD: rootLimit, MaxCalls: s.t.Budget.MaxIterations}, p)
	e.recordShadow(ctx, p.Purpose, p.Tier)
	if err != nil {
		switch {
		case errors.Is(err, budget.ErrTaskBudget), errors.Is(err, budget.ErrDailyBudget), errors.Is(err, budget.ErrIterations):
			s.fatal = err
			return nil, rpc.Errf(rpc.CodeBudget, err.Error(), "budget exceeded")
		case errors.Is(err, budget.ErrHalted):
			s.fatal = err
			return nil, rpc.Errf(rpc.CodeHalted, "halted", "kill switch active")
		case errors.Is(err, gateway.ErrInvalidOutput):
			// Not fatal here: the worker's cascade may retry on a stronger tier.
			return nil, rpc.Errf(rpc.CodeProvider, "invalid_model_output", "model output failed schema validation")
		case ctx.Err() != nil:
			return nil, rpc.Errf(rpc.CodeInternal, "cancelled", "cancelled")
		}
		// No route / all routes failed for this tier: the worker may escalate to another tier.
		return nil, rpc.Errf(rpc.CodeProvider, reasonCode(err), "provider failed: %s", truncate(err.Error(), 300))
	}
	budget, over := e.cfg.PromptBudgetChars[p.Tier], false
	over = budget > 0 && size > budget
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, s.t.ID, ptr(stepID(s)), "MODEL_CALLED", "core", map[string]any{
			"tier": p.Tier, "route_id": res.RouteID, "structured": len(res.Structured) > 0, "purpose": p.Purpose,
			"prompt_chars": size, "over_budget": over}) // OL-05: prompt size per step is recorded in the trace
		return err
	})
	return res, nil
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type toolCall struct {
	ToolID      string          `json:"tool_id"`
	ToolVersion string          `json:"tool_version,omitempty"`
	Arguments   json.RawMessage `json:"arguments"`
	// approval.request only
	Justification string `json:"justification,omitempty"`
}

type fetchArgs struct {
	URL string `json:"url"`
}
type readArgs struct {
	Path string `json:"path"`
}
type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// serveTool authorizes and executes (or queues for approval) one tool call.
// The call must be the current step's tool with the planned arguments, at most
// once per step; tier and side-effect class come from the trusted manifest.
func (e *Engine) serveTool(ctx context.Context, s *session, g *policy.Grant, req *rpc.Envelope, forceApproval bool) (any, *rpc.Error) {
	var c toolCall
	if err := strictDecode(req.Params, &c); err != nil {
		return nil, rpc.Errf(rpc.CodeInvalidParams, "invalid_params", "tool.invoke: invalid params")
	}
	spec, ok := policy.Lookup(c.ToolID)
	if !ok || !g.Scopes.HasTool(c.ToolID) {
		return nil, e.deny(ctx, s, req.Method, "tool_not_granted", map[string]any{"tool_id": c.ToolID})
	}
	if s.step == nil || s.step.ToolID == nil || *s.step.ToolID != c.ToolID {
		return nil, e.deny(ctx, s, req.Method, "tool_not_in_current_step", map[string]any{"tool_id": c.ToolID})
	}
	if s.invoked {
		return nil, e.deny(ctx, s, req.Method, "one_tool_call_per_step", map[string]any{"tool_id": c.ToolID})
	}
	args, target, rerr := e.validateArgs(s, g, c)
	if rerr != "" {
		return nil, e.deny(ctx, s, req.Method, rerr, map[string]any{"tool_id": c.ToolID})
	}
	tier := spec.Tier(args)
	if forceApproval && tier == policy.A0 {
		tier = policy.A2
	}
	if err := policy.CheckTier(tier); err != nil {
		return nil, e.deny(ctx, s, req.Method, err.Error(), map[string]any{"tool_id": c.ToolID, "tier": tier})
	}
	if tier == policy.A2 && s.t.AgentRole == "lab" {
		return nil, e.deny(ctx, s, req.Method, "lab_no_approval", map[string]any{"tool_id": c.ToolID}) // the Lab never asks the owner
	}
	s.invoked = true
	if tier == policy.A2 {
		act := policy.Action{ToolID: spec.ID, ToolVersion: spec.Version, Arguments: args, Target: target,
			SideEffect: spec.SideEffect, TaskID: s.t.ID, StepID: s.step.ID, GrantID: g.ID}
		var ap *policy.Approval
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			var err error
			if ap, err = policy.RequestApproval(ctx, tx, act, int64(e.cfg.Approvals.TTLSeconds)*1000); err != nil {
				return err
			}
			if err := task.UpdateStep(ctx, tx, s.step, task.StepWaitingApproval, "", "", "", "APPROVAL_REQUESTED", map[string]any{
				"approval_id": ap.ID, "tool_id": spec.ID, "target": target, "action_hash": ap.ActionHash}); err != nil {
				return err
			}
			return policy.Audit(ctx, tx, "APPROVAL_REQUESTED", "core", s.t.ID, "", map[string]any{"approval_id": ap.ID, "tool_id": spec.ID, "target": target})
		})
		if err != nil {
			return nil, rpc.Errf(rpc.CodeInternal, "internal", "approval request failed")
		}
		s.approvalID = ap.ID
		return map[string]any{"status": "waiting_approval", "approval_id": ap.ID}, nil
	}
	if spec.ID == "agent.delegate" {
		id, err := e.delegate(ctx, s.t, s.step, args)
		if err != nil {
			if errors.Is(err, errDelegationDenied) {
				return nil, e.deny(ctx, s, req.Method, err.Error(), map[string]any{"tool_id": spec.ID})
			}
			return map[string]any{"status": "error", "reason_code": reasonCode(err)}, nil
		}
		s.waitID = id
		return map[string]any{"status": "waiting", "reason_code": "subagent_running"}, nil
	}
	if spec.ID == "browser.mfa" {
		if _, err := e.browserSession(ctx, s.t); err != nil {
			return map[string]any{"status": "error", "reason_code": reasonCode(browserErr(err))}, nil
		}
		id, err := e.startMFA(ctx, s.t, s.step, args)
		if err != nil {
			return map[string]any{"status": "error", "reason_code": reasonCode(err)}, nil
		}
		s.waitID = id
		return map[string]any{"status": "waiting", "reason_code": "owner_one_time_code"}, nil
	}
	if spec.ID == "wait.event" || spec.ID == "wait.until" {
		id, err := e.createWait(ctx, s.t, s.step, spec.ID, args)
		if err != nil {
			return map[string]any{"status": "error", "reason_code": reasonCode(err)}, nil
		}
		s.waitID = id
		return map[string]any{"status": "waiting", "reason_code": "supervisor_wait"}, nil
	}
	out, err := e.execute0(ctx, s.t, s.step, spec, args)
	if err != nil {
		return map[string]any{"status": "error", "reason_code": reasonCode(err)}, nil
	}
	s.executed = true
	return map[string]any{"status": "ok", "output": out}, nil
}

// validateArgs decodes tool arguments strictly and requires them to match the
// planned step, so content read during the task cannot redirect a tool.
func (e *Engine) validateArgs(s *session, g *policy.Grant, c toolCall) (map[string]any, string, string) {
	switch c.ToolID {
	case "web.fetch":
		var a fetchArgs
		if strictDecode(c.Arguments, &a) != nil || a.URL == "" {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil {
			return nil, "", "arguments_differ_from_plan"
		}
		if s.plan.URL != nil && *s.plan.URL != "" {
			if *s.plan.URL != a.URL {
				return nil, "", "arguments_differ_from_plan"
			}
		} else if !e.hasProvenance(s.t, a.URL) {
			// A URL chosen at step time must have been written by the owner in the
			// objective or recorded by the Core earlier in this task (search result or
			// page link), exactly (ADR-0017).
			return nil, "", "argument_without_provenance"
		}
		u, err := url.Parse(a.URL)
		if err != nil || tools.CheckURL(u, g.Scopes.AllowDomains) != nil {
			return nil, "", "domain_not_allowed"
		}
		return map[string]any{"url": a.URL}, a.URL, ""
	case "web.search":
		var a struct {
			Query string `json:"query"`
		}
		if strictDecode(c.Arguments, &a) != nil || strings.TrimSpace(a.Query) == "" || len(a.Query) > 300 {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Query == nil || *s.plan.Query != a.Query {
			return nil, "", "arguments_differ_from_plan"
		}
		return map[string]any{"query": a.Query}, a.Query, ""
	case "data.read":
		var a readArgs
		if strictDecode(c.Arguments, &a) != nil {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Path == nil || *s.plan.Path != a.Path {
			return nil, "", "arguments_differ_from_plan"
		}
		p, err := tools.CleanPath(a.Path)
		if err != nil {
			return nil, "", "bad_path"
		}
		return map[string]any{"path": p}, p, ""
	case "agent.delegate":
		var a struct {
			Brief   string   `json:"brief"`
			Tools   []string `json:"tools"`
			Profile string   `json:"profile"`
		}
		if strictDecode(c.Arguments, &a) != nil || strings.TrimSpace(a.Brief) == "" {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Brief == nil || *s.plan.Brief != a.Brief {
			return nil, "", "arguments_differ_from_plan"
		}
		var planned struct {
			Profile string `json:"profile"`
		}
		if s.plan.Input != nil {
			json.Unmarshal([]byte(*s.plan.Input), &planned)
		}
		if a.Profile != planned.Profile {
			return nil, "", "arguments_differ_from_plan"
		}
		child := a.Tools
		var profileTools []string
		if a.Profile != "" {
			sp, ok := e.specialist(a.Profile)
			if !ok {
				return nil, "", "unknown_specialist"
			}
			profileTools = sp.Tools
			if len(child) == 0 {
				for _, tl := range sp.Tools {
					if g.Scopes.HasTool(tl) {
						child = append(child, tl)
					}
				}
			}
		}
		if len(child) == 0 && a.Profile == "" {
			child = g.Scopes.Tools
		}
		var out []any
		for _, tl := range child {
			if !g.Scopes.HasTool(tl) {
				return nil, "", "grant_widening" // a subagent gets at most the parent's tools
			}
			if profileTools != nil && !slices.Contains(profileTools, tl) {
				return nil, "", "grant_widening" // and at most its profile's tools
			}
			out = append(out, tl)
		}
		if len(out) == 0 {
			return nil, "", "no_tools_for_subagent"
		}
		args := map[string]any{"brief": a.Brief, "tools": out}
		if a.Profile != "" {
			args["profile"] = a.Profile
		}
		return args, "subagent", ""
	case "wait.event":
		var a struct {
			Key string `json:"key"`
		}
		if strictDecode(c.Arguments, &a) != nil || !eventKeyRe.MatchString(a.Key) {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Key == nil || *s.plan.Key != a.Key {
			return nil, "", "arguments_differ_from_plan"
		}
		return map[string]any{"key": a.Key}, a.Key, ""
	case "wait.until":
		var a struct {
			Until string `json:"until"`
		}
		if strictDecode(c.Arguments, &a) != nil {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Until == nil || *s.plan.Until != a.Until {
			return nil, "", "arguments_differ_from_plan"
		}
		at, err := time.Parse(time.RFC3339, a.Until)
		if err != nil || at.After(time.Now().Add(maxWait)) {
			return nil, "", "invalid_wait_time"
		}
		return map[string]any{"until": a.Until, "until_ms": at.UnixMilli()}, a.Until, ""
	case "calc":
		var a struct {
			Expression string `json:"expression"`
		}
		if strictDecode(c.Arguments, &a) != nil || strings.TrimSpace(a.Expression) == "" {
			return nil, "", "invalid_arguments"
		}
		// calc is pure (no I/O, no side effect), so a step-time expression is allowed.
		return map[string]any{"expression": a.Expression}, "calc", ""
	case "workspace.read":
		var a readArgs
		if strictDecode(c.Arguments, &a) != nil {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Path == nil || *s.plan.Path != a.Path {
			return nil, "", "arguments_differ_from_plan"
		}
		p, err := tools.CleanPath(a.Path)
		if err != nil {
			return nil, "", "bad_path"
		}
		return map[string]any{"path": p}, p, ""
	case "workspace.write":
		var a writeArgs
		if strictDecode(c.Arguments, &a) != nil {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil || s.plan.Path == nil || *s.plan.Path != a.Path {
			return nil, "", "arguments_differ_from_plan"
		}
		p, err := tools.CleanPath(a.Path)
		if err != nil {
			return nil, "", "bad_path"
		}
		if len(a.Content) > tools.MaxFileBytes {
			return nil, "", "content_too_large"
		}
		return map[string]any{"path": p, "content": a.Content}, p, ""
	}
	if strings.HasPrefix(c.ToolID, "mcp.") {
		return validateMCPArgs(s, c)
	}
	if strings.HasPrefix(c.ToolID, "l2.") {
		return validateL2Args(s, c)
	}
	if strings.HasPrefix(c.ToolID, "browser.") {
		return e.validateBrowserArgs(s, c)
	}
	if strings.HasPrefix(c.ToolID, "connector.") {
		return validateMCPArgs(s, c) // planned input, decided at planning time
	}
	return nil, "", "unknown_tool"
}

// hasProvenance reports whether url was recorded by the Core in this task as a
// search result or a page link.
func (e *Engine) hasProvenance(t *task.Task, u string) bool {
	for _, x := range ObjectiveURLs(t.Objective) {
		if x == u {
			return true // owner-provided, verbatim
		}
	}
	rows, err := e.st.R.Query("select state_json from checkpoints where task_id=? and label='links'", t.ID)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var l struct {
			URLs []string `json:"urls"`
		}
		if json.Unmarshal([]byte(raw), &l) == nil {
			for _, x := range l.URLs {
				if x == u {
					return true
				}
			}
		}
	}
	return false
}

// execute0 runs an authorized A0 (or approved) tool call and records its
// outcome on the step. Writes are preceded by a durable intent (§8.1).
func (e *Engine) execute0(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	if strings.HasPrefix(spec.ID, "mcp.") {
		return e.executeMCP(ctx, t, st, spec, args)
	}
	if strings.HasPrefix(spec.ID, "l2.") {
		return e.executeL2(ctx, t, st, spec, args)
	}
	if strings.HasPrefix(spec.ID, "browser.") {
		return e.executeBrowser(ctx, t, st, spec, args)
	}
	if spec.ID == ConnectorGitHubTool {
		return e.executeGitHub(ctx, t, st, spec, args)
	}
	switch spec.ID {
	case "wait.event", "wait.until", "agent.delegate":
		return nil, errWaitTool // handled in serveTool before execution
	case "web.search", "data.read", "calc":
		return e.executeInfo(ctx, t, st, spec, args)
	case "web.fetch", "workspace.read":
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_STARTED", "core", map[string]any{"tool_id": spec.ID,
				"idempotency_key": st.IdempotencyKey, "target": firstString(args, "url", "path")})
			return err
		})
		var rec obsRecord
		var summary map[string]any
		var hash string
		var links []string
		if spec.ID == "web.fetch" {
			res, err := e.fetch.Fetch(ctx, args["url"].(string), t.Scope.AllowDomains)
			if err == nil && res.Status >= 400 {
				// An error page is not the requested content: never summarize it as such.
				err = fmt.Errorf("tool_failed: HTTP %d from %s", res.Status, res.FinalURL)
			}
			if err != nil {
				e.toolFailed(ctx, t, st, spec.ID, err)
				return nil, err
			}
			content := res.Text
			if res.Title != "" {
				content = "Title: " + res.Title + "\n\n" + content
			}
			rec = obsRecord{Source: res.FinalURL, Content: truncate(content, maxObservationChars)}
			summary = map[string]any{"final_url": res.FinalURL, "status": res.Status, "bytes": res.Bytes, "sha256": res.SHA256, "title": res.Title,
				"links": len(res.Links)}
			hash = res.SHA256
			links = res.Links
		} else {
			res, err := e.ws.Read(t.ID, args["path"].(string))
			if err != nil {
				e.toolFailed(ctx, t, st, spec.ID, err)
				return nil, err
			}
			rec = obsRecord{Source: res.Path, Content: truncate(res.Content, maxObservationChars)}
			summary = map[string]any{"path": res.Path, "bytes": res.Bytes, "sha256": res.SHA256}
			hash = res.SHA256
		}
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
			if err != nil {
				return err
			}
			if len(links) > 0 {
				if _, err := putCheckpoint(ctx, tx, t.ID, st.ID, "links", map[string]any{"urls": links}); err != nil {
					return err
				}
			}
			summary["tool_id"] = spec.ID
			return task.UpdateStep(ctx, tx, st, st.State, ck, hash, "", "TOOL_SUCCEEDED", summary)
		})
		return summary, err
	case "workspace.write":
		in := writeIntent{Path: args["path"].(string), Content: args["content"].(string)}
		in.SHA256 = tools.Hash([]byte(in.Content))
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "write_intent", in)
			if err != nil {
				return err
			}
			return task.UpdateStep(ctx, tx, st, st.State, ck, in.SHA256, "", "TOOL_INTENT", map[string]any{
				"tool_id": spec.ID, "path": in.Path, "sha256": in.SHA256, "bytes": len(in.Content)})
		})
		if err != nil {
			return nil, err
		}
		return e.applyWrite(ctx, t, st, in)
	}
	return nil, fmt.Errorf("unknown_tool: %s", spec.ID)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

func (e *Engine) toolFailed(ctx context.Context, t *task.Task, st *task.Step, tool string, err error) {
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err2 := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_FAILED", "core", map[string]any{"tool_id": tool, "reason": reasonCode(err),
			"detail": truncate(err.Error(), 300)})
		return err2
	})
}

// applyWrite performs a recorded write intent; an identical existing file is
// skipped, so replay after a crash never writes twice.
func (e *Engine) applyWrite(ctx context.Context, t *task.Task, st *task.Step, in writeIntent) (any, error) {
	res, err := e.ws.Write(t.ID, in.Path, in.Content)
	if err != nil {
		e.toolFailed(ctx, t, st, "workspace.write", err)
		return nil, err
	}
	summary := map[string]any{"tool_id": "workspace.write", "path": res.Path, "sha256": res.SHA256, "bytes": res.Bytes, "skipped": res.Skipped}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		return task.UpdateStep(ctx, tx, st, st.State, "", "", "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}

// replayIntent completes a write step from its recorded intent without the worker.
func (e *Engine) replayIntent(ctx context.Context, t *task.Task, st *task.Step, in writeIntent) error {
	if _, err := e.applyWrite(ctx, t, st, in); err != nil {
		e.failStep(ctx, st, reasonCode(err))
		return err
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		return task.UpdateStep(ctx, tx, st, task.StepSucceeded, "", "", "", "STEP_SUCCEEDED", map[string]any{"replayed_intent": true})
	})
}

// executeApproved runs exactly the approved action after re-validating policy:
// the tool is still granted to the task, the tier is still A2, and the action
// hashes to the approved hash (checked by Consume).
func (e *Engine) executeApproved(ctx context.Context, t *task.Task, st *task.Step, ap *policy.Approval) error {
	spec, ok := policy.Lookup(ap.ToolID)
	scope := policy.Scopes{Tools: t.Scope.Tools}
	if !ok || !scope.HasTool(ap.ToolID) || spec.Version != ap.ToolVersion || spec.Tier(ap.Parameters) != policy.A2 {
		e.failStep(ctx, st, "approval_policy_changed")
		return errors.New("step_failed: approval no longer matches policy")
	}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := policy.Consume(ctx, tx, ap.ID, ap.Action()); err != nil {
			return err
		}
		if err := policy.Audit(ctx, tx, "APPROVAL_CONSUMED", "core", t.ID, "", map[string]any{"approval_id": ap.ID, "action_hash": ap.ActionHash}); err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, task.StepStarted, "", "", "", "STEP_STARTED", map[string]any{"approval_id": ap.ID})
	})
	if err != nil {
		e.failStep(ctx, st, reasonCode(err))
		return fmt.Errorf("step_failed: %w", err)
	}
	if _, err := e.execute0(ctx, t, st, spec, ap.Parameters); err != nil {
		e.failStep(ctx, st, reasonCode(err))
		return err
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		return task.UpdateStep(ctx, tx, st, task.StepSucceeded, "", "", "", "STEP_SUCCEEDED", map[string]any{"approval_id": ap.ID})
	})
}

// executeInfo runs the information tools whose output becomes an observation:
// web.search (results also recorded as URL provenance), data.read, calc.
func (e *Engine) executeInfo(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	var rec obsRecord
	var summary map[string]any
	var links []string
	switch spec.ID {
	case "web.search":
		if e.srch == nil {
			err := errors.New("tool_failed: web.search is not configured (tools.web.search.provider)")
			e.toolFailed(ctx, t, st, spec.ID, err)
			return nil, err
		}
		q := args["query"].(string)
		results, err := e.srch.Search(ctx, q, e.cfg.Tools.WebSearch.MaxResults)
		if err != nil {
			err = fmt.Errorf("tool_failed: %v", err)
			e.toolFailed(ctx, t, st, spec.ID, err)
			return nil, err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Search results for %q (%s):\n", q, e.srch.Name())
		for i, r := range results {
			fmt.Fprintf(&sb, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, r.Snippet)
			links = append(links, r.URL)
		}
		if len(results) == 0 {
			sb.WriteString("(no results)\n")
		}
		rec = obsRecord{Source: "search:" + q, Content: truncate(sb.String(), maxObservationChars)}
		summary = map[string]any{"provider": e.srch.Name(), "results": len(results)}
	case "data.read":
		d, err := e.ws.ReadData(t.ID, args["path"].(string))
		if err != nil {
			err = fmt.Errorf("tool_failed: %v", err)
			e.toolFailed(ctx, t, st, spec.ID, err)
			return nil, err
		}
		b, _ := json.MarshalIndent(d, "", " ")
		rec = obsRecord{Source: d.Path, Content: truncate(string(b), maxObservationChars)}
		summary = map[string]any{"path": d.Path, "rows": d.RowCount, "columns": len(d.Columns), "sha256": d.SHA256}
	case "calc":
		r, err := tools.Calc(args["expression"].(string))
		if err != nil {
			err = fmt.Errorf("tool_failed: %v", err)
			e.toolFailed(ctx, t, st, spec.ID, err)
			return nil, err
		}
		exact := "exact"
		if !r.Exact {
			exact = "rounded to 20 decimal places; exact fraction " + r.Fraction
		}
		rec = obsRecord{Source: "calc", Content: fmt.Sprintf("%s = %s (%s)", r.Expression, r.Result, exact)}
		summary = map[string]any{"expression": r.Expression, "result": r.Result, "exact": r.Exact}
	}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
		if err != nil {
			return err
		}
		if len(links) > 0 {
			if _, err := putCheckpoint(ctx, tx, t.ID, st.ID, "links", map[string]any{"urls": links}); err != nil {
				return err
			}
		}
		summary["tool_id"] = spec.ID
		return task.UpdateStep(ctx, tx, st, st.State, ck, tools.Hash([]byte(rec.Content)), "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}
