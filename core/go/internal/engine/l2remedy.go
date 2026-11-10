package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// startToolRemedy acts on an R3 remedy (Blueprint 8A.5): the worker drafts an
// L2 tool from the limitation and its failed tasks; the Core validates it in the
// sandbox (one repair round with the problems found); a passing draft waits
// for the owner's approval. Nothing is registered here.
func (e *Engine) startToolRemedy(ctx context.Context, remedyID, limID string) error {
	if e.sbx == nil {
		return ErrL2Unavailable
	}
	var l *Limitation
	ls, err := e.ListLimitations(ctx, "")
	if err != nil {
		return err
	}
	for _, x := range ls {
		if x.ID == limID {
			l = x
		}
	}
	if l == nil {
		return task.ErrNotFound
	}
	failed := e.exemplars(ctx, l.TaskClass, "failed", 3)
	var examples []map[string]any
	for _, x := range failed {
		examples = append(examples, map[string]any{"objective": truncate(x.Objective, 500), "steps": x.Steps, "reason_code": x.Code})
	}
	it, err := e.improvementTask(ctx, "L2 tool remedy for "+limID, anyPrivate(failed) || l.DetectedBy == "owner_feedback")
	if err != nil {
		return err
	}
	var cause error
	defer func() { e.finishImprovementTask(ctx, it.ID, cause) }()
	params := map[string]any{"need": l.Title, "task_class": l.TaskClass, "diagnosis": l.Diagnosis, "owner_note": l.Evidence["owner_note"],
		"failed": examples, "allowed_imports": l2Allowed}
	var draft *L2Draft
	var report map[string]any
	for attempt := 0; attempt < 2; attempt++ {
		s, err := e.newSession(ctx, it)
		if err != nil {
			cause = err
			return err
		}
		raw, err := e.callWorker(ctx, s, "task.build_tool", params)
		if err != nil {
			cause = err
			return err
		}
		var d L2Draft
		if err := json.Unmarshal(raw, &d); err != nil {
			cause = fmt.Errorf("invalid tool draft: %v", err)
			return cause
		}
		var problems []string
		report, problems = e.validateL2(ctx, &d)
		draft = &d
		if len(problems) == 0 {
			break
		}
		if attempt == 1 {
			cause = fmt.Errorf("the tool draft still fails its gates: %s", strings.Join(firstN(problems, 3), "; "))
			e.st.Write(ctx, func(tx *sql.Tx) error {
				now := ids.NowMs()
				tx.ExecContext(ctx, "update limitation_remedies set status='escalated', artifact_json=?, description=description || ?, updated_at=? where id=?",
					js(map[string]any{"report": report}), " The drafted tool failed its sandbox gates twice (owner action needed).", now, remedyID)
				_, err := tx.ExecContext(ctx, "update limitations set status='escalated', updated_at=? where id=?", now, limID)
				return err
			})
			return nil
		}
		prev, _ := json.Marshal(d)
		params["previous_draft"] = string(prev)
		params["problems"] = problems
	}
	tool, err := e.saveL2Draft(ctx, draft, report, limID, remedyID)
	if err != nil {
		cause = err
		return err
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		art := map[string]any{"stage": "awaiting_owner_approval", "tool_id": tool.ID, "tool": "l2." + tool.Name, "version": tool.Version,
			"sha256": tool.SourceSHA256}
		_, err := tx.ExecContext(ctx, "update limitation_remedies set artifact_json=?, updated_at=? where id=?", js(art), ids.NowMs(), remedyID)
		return err
	})
}

// startL2Retest runs the limitation's failed tasks again with the approved
// tool granted (and recent successes as a regression sample). With no task to
// re-run, the sandbox tests are the only evidence and the remedy is promoted
// on them, saying so.
func (e *Engine) startL2Retest(ctx context.Context, remedyID, limID string, tool *L2Tool) error {
	var l *Limitation
	if ls, err := e.ListLimitations(ctx, ""); err == nil {
		for _, x := range ls {
			if x.ID == limID {
				l = x
			}
		}
	}
	if l == nil {
		return task.ErrNotFound
	}
	toolID := "l2." + tool.Name
	var fixtures []map[string]any
	for _, kind := range []string{"exposing", "regression"} {
		state, n := "failed", max(1, e.ledgerCfg.RetestRuns-2)
		if kind == "regression" {
			state, n = "completed", 2
		}
		for _, x := range e.exemplars(ctx, l.TaskClass, state, n) {
			t, _, err := e.submitRetest(ctx, x, toolID)
			if err != nil {
				e.log.Info("re-test skipped", "exemplar", x.ID, "err", err)
				continue
			}
			fixtures = append(fixtures, map[string]any{"task_id": t.ID, "kind": kind, "from": x.ID})
		}
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		now := ids.NowMs()
		art := map[string]any{"tool_id": tool.ID, "tool": toolID, "version": tool.Version, "sha256": tool.SourceSHA256, "fixtures": fixtures}
		if len(fixtures) == 0 {
			after := map[string]any{"metric": "sandbox_tests", "value": 1.0, "note": "no failed task of this class to re-run; promoted on the sandbox tests only",
				"report": tool.Report}
			tx.ExecContext(ctx, "update limitation_remedies set status='promoted', artifact_json=?, after_json=?, updated_at=? where id=?", js(art), js(after), now, remedyID)
			tx.ExecContext(ctx, "update limitations set status='resolved', result_json=?, closed_at=?, version=version+1, updated_at=? where id=?", js(after), now, now, limID)
			return policy.Audit(ctx, tx, "REMEDY_PROMOTED", "core", "", "", map[string]any{"limitation": limID, "class": "R3", "tool": toolID, "evidence": "sandbox tests"})
		}
		if _, err := tx.ExecContext(ctx, "update limitation_remedies set status='testing', artifact_json=?, before_json=?, updated_at=? where id=?",
			js(art), js(l.Baseline), now, remedyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update limitations set status='remedy_testing', updated_at=? where id=?", now, limID); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "REMEDY_STARTED", "core", "", "", map[string]any{"limitation": limID, "class": "R3", "tool": toolID, "fixtures": len(fixtures)})
	})
}
