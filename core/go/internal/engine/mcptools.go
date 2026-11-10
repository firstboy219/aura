package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// externalIntent is recorded before an MCP call with a side effect, so a crash
// between the call and its recorded outcome is detectable (Blueprint §8.1).
type externalIntent struct {
	ToolID    string         `json:"tool_id"`
	Arguments map[string]any `json:"arguments"`
}

// validateMCPArgs requires an MCP call's arguments to equal the planned input.
func validateMCPArgs(s *session, c toolCall) (map[string]any, string, string) {
	var a struct {
		Arguments map[string]any `json:"arguments"`
	}
	if strictDecode(c.Arguments, &a) != nil || a.Arguments == nil {
		return nil, "", "invalid_arguments"
	}
	if s.plan == nil || s.plan.Input == nil {
		return nil, "", "arguments_differ_from_plan"
	}
	var planned map[string]any
	if json.Unmarshal([]byte(*s.plan.Input), &planned) != nil || !reflect.DeepEqual(planned, a.Arguments) {
		return nil, "", "arguments_differ_from_plan"
	}
	return a.Arguments, c.ToolID, ""
}

func splitMCP(id string) (server, tool string, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(id, "mcp."), ".", 2)
	if !strings.HasPrefix(id, "mcp.") || len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// executeMCP calls an owner-declared MCP tool. Side-effecting calls record an
// intent first; the result is untrusted content.
func (e *Engine) executeMCP(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	server, tool, ok := splitMCP(spec.ID)
	client := e.mcp[server]
	if !ok || client == nil {
		err := fmt.Errorf("tool_failed: MCP server for %s is not configured", spec.ID)
		e.toolFailed(ctx, t, st, spec.ID, err)
		return nil, err
	}
	if spec.SideEffect == "write_external" || spec.SideEffect == "write_idempotent" {
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "external_intent", externalIntent{ToolID: spec.ID, Arguments: args})
			if err != nil {
				return err
			}
			return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_INTENT", map[string]any{"tool_id": spec.ID, "side_effect": spec.SideEffect})
		})
		if err != nil {
			return nil, err
		}
	} else {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_STARTED", "core", map[string]any{"tool_id": spec.ID, "idempotency_key": st.IdempotencyKey})
			return err
		})
	}
	res, err := client.Call(ctx, tool, args)
	if err == nil && res.IsError {
		err = fmt.Errorf("tool_failed: %s reported an error: %s", spec.ID, truncate(res.Text, 300))
	}
	if err != nil {
		if !strings.HasPrefix(err.Error(), "tool_failed") {
			err = fmt.Errorf("tool_failed: %v", err)
		}
		e.toolFailed(ctx, t, st, spec.ID, err)
		return nil, err
	}
	rec := obsRecord{Source: spec.ID, Content: truncate(res.Text, maxObservationChars)}
	summary := map[string]any{"tool_id": spec.ID, "bytes": len(res.Text)}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
		if err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}

// outcomeUnknown handles a write_external step interrupted by a Core crash: the
// outcome cannot be established, so it is never retried automatically. The
// owner gets an approval for the exact recorded action (approve = run it again,
// reject = treat as failed).
func (e *Engine) outcomeUnknown(ctx context.Context, tx *sql.Tx, taskID string, s *task.Step) error {
	var raw string
	if s.OutcomeRef == nil || tx.QueryRowContext(ctx, "select state_json from checkpoints where id=?", *s.OutcomeRef).Scan(&raw) != nil {
		return task.UpdateStep(ctx, tx, s, task.StepFailed, "", "", "outcome_unknown_without_intent", "STEP_FAILED", nil)
	}
	var in externalIntent
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return err
	}
	spec, ok := policy.Lookup(in.ToolID)
	if !ok {
		return task.UpdateStep(ctx, tx, s, task.StepFailed, "", "", "outcome_unknown_tool_removed", "STEP_FAILED", nil)
	}
	var grantID string
	if err := tx.QueryRowContext(ctx, "select id from capability_grants where task_id=? order by created_at desc limit 1", taskID).Scan(&grantID); err != nil {
		return err
	}
	act := policy.Action{ToolID: spec.ID, ToolVersion: spec.Version, Arguments: in.Arguments, Target: spec.ID,
		SideEffect: spec.SideEffect, TaskID: taskID, StepID: s.ID, GrantID: grantID}
	ap, err := policy.RequestApproval(ctx, tx, act, int64(e.cfg.Approvals.TTLSeconds)*1000)
	if err != nil {
		return err
	}
	return task.UpdateStep(ctx, tx, s, task.StepWaitingApproval, "", "", "OUTCOME_UNKNOWN", "OUTCOME_UNKNOWN", map[string]any{
		"approval_id": ap.ID, "tool_id": spec.ID, "note": "the Core stopped during this external action; it was not repeated automatically"})
}
