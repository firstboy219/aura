package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/rpc"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/worker"
)

const maxObservationChars = 50_000

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
	obs, err := e.observations(ctx, t.ID, st.StepNo)
	if err != nil {
		return err
	}
	raw, err := e.callWorker(ctx, s, "task.step", map[string]any{"objective": t.Objective, "step": ps, "observations": obs})
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
	case "fail":
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
func (e *Engine) observations(ctx context.Context, taskID string, before int) ([]observation, error) {
	steps, err := task.Steps(ctx, e.st.R, taskID)
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
	res, err := e.gw.Complete(ctx, gateway.Budget{TaskID: s.t.ID, StepID: stepID(s), TaskLimitUSD: s.t.Budget.MaxCostUSD,
		MaxCalls: s.t.Budget.MaxIterations}, p)
	if err != nil {
		switch {
		case errors.Is(err, budget.ErrTaskBudget), errors.Is(err, budget.ErrDailyBudget), errors.Is(err, budget.ErrIterations):
			s.fatal = err
			return nil, rpc.Errf(rpc.CodeBudget, err.Error(), "budget exceeded")
		case errors.Is(err, budget.ErrHalted):
			s.fatal = err
			return nil, rpc.Errf(rpc.CodeHalted, "halted", "kill switch active")
		case errors.Is(err, gateway.ErrInvalidOutput):
			s.fatal = err
			return nil, rpc.Errf(rpc.CodeProvider, "invalid_model_output", "model output failed schema validation")
		case ctx.Err() != nil:
			return nil, rpc.Errf(rpc.CodeInternal, "cancelled", "cancelled")
		}
		s.fatal = err
		return nil, rpc.Errf(rpc.CodeProvider, reasonCode(err), "provider failed")
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, s.t.ID, ptr(stepID(s)), "MODEL_CALLED", "core", map[string]any{
			"tier": p.Tier, "route_id": res.RouteID, "structured": len(res.Structured) > 0})
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
	spec, ok := policy.Manifest[c.ToolID]
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
		if s.plan == nil || s.plan.URL == nil || *s.plan.URL != a.URL {
			return nil, "", "arguments_differ_from_plan"
		}
		u, err := url.Parse(a.URL)
		if err != nil || tools.CheckURL(u, g.Scopes.AllowDomains) != nil {
			return nil, "", "domain_not_allowed"
		}
		return map[string]any{"url": a.URL}, a.URL, ""
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
	return nil, "", "unknown_tool"
}

// execute0 runs an authorized A0 (or approved) tool call and records its
// outcome on the step. Writes are preceded by a durable intent (§8.1).
func (e *Engine) execute0(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	switch spec.ID {
	case "web.fetch", "workspace.read":
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_STARTED", "core", map[string]any{"tool_id": spec.ID,
				"idempotency_key": st.IdempotencyKey, "target": firstString(args, "url", "path")})
			return err
		})
		var rec obsRecord
		var summary map[string]any
		var hash string
		if spec.ID == "web.fetch" {
			res, err := e.fetch.Fetch(ctx, args["url"].(string), t.Scope.AllowDomains)
			if err != nil {
				e.toolFailed(ctx, t, st, spec.ID, err)
				return nil, err
			}
			content := res.Text
			if res.Title != "" {
				content = "Title: " + res.Title + "\n\n" + content
			}
			rec = obsRecord{Source: res.FinalURL, Content: truncate(content, maxObservationChars)}
			summary = map[string]any{"final_url": res.FinalURL, "status": res.Status, "bytes": res.Bytes, "sha256": res.SHA256, "title": res.Title}
			hash = res.SHA256
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
	spec, ok := policy.Manifest[ap.ToolID]
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
