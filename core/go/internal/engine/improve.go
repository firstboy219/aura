package engine

// The act / re-test / promote-or-rollback half of the loop (Blueprint §8A.5).
// R2 (skill, L1) is the one remedy class that acts automatically in P3a: a
// corrective skill is drafted from the class's failed and successful tasks, then
// re-tested on the tasks that exposed the limitation plus a regression sample.
// It is promoted only if the defining metric improves beyond the noise margin
// with no regression; otherwise the skill is rolled back and the reason is
// recorded. Improvement work runs at the lowest priority inside its own budget.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/taxonomy"
)

const improveEvery = 30 * time.Second

// improveTick runs the loop at most every 30 s and only if enabled.
func (e *Engine) improveTick(ctx context.Context) {
	if !e.cfg.Improvement.Enabled || time.Since(e.lastImprove) < improveEvery {
		return
	}
	e.lastImprove = time.Now()
	e.RunImprovementCycle(ctx)
}

// RunImprovementCycle is one pass: Detect → Diagnose → Act → Evaluate.
func (e *Engine) RunImprovementCycle(ctx context.Context) {
	// One cycle at a time: a periodic tick and an explicit run must never act on
	// the same remedy twice (found by the RS-6 test: a tool drafted twice).
	if !e.improveMu.TryLock() {
		return
	}
	defer e.improveMu.Unlock()
	e.CloseRecovered(ctx) // close with evidence first, so recovered problems are not re-diagnosed
	if _, err := e.Detect(ctx); err != nil {
		e.log.Warn("limitation detection", "err", err)
		return
	}
	if _, err := e.Diagnose(ctx); err != nil {
		e.log.Warn("limitation diagnosis", "err", err)
	}
	e.ActOnRemedies(ctx)
	e.EvaluateRemedies(ctx)
	e.EvaluateLab(ctx)
	e.EvaluateCanaries(ctx)
}

type exemplar struct {
	ID        string
	Objective string
	State     string
	Code      string
	Scope     task.Scope
	Budget    task.Budget
	Steps     []string
}

// exemplars loads finished main tasks of a class (failed or completed), newest first.
func (e *Engine) exemplars(ctx context.Context, class, state string, limit int) []exemplar {
	cond := "state = 'COMPLETED'"
	if state == "failed" {
		cond = "state in ('FAILED','BUDGET_EXCEEDED')"
	}
	rows, err := e.st.R.QueryContext(ctx, `select id, objective, state, coalesce(reason_code,''), scope_json, budget_json from tasks
		where task_class = ? and parent_task_id is null and agent_role = 'main' and `+cond+` order by created_at desc limit ?`, class, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []exemplar
	for rows.Next() {
		var x exemplar
		var sc, bu string
		rows.Scan(&x.ID, &x.Objective, &x.State, &x.Code, &sc, &bu)
		json.Unmarshal([]byte(sc), &x.Scope)
		json.Unmarshal([]byte(bu), &x.Budget)
		out = append(out, x)
	}
	for i := range out {
		steps, _ := task.Steps(ctx, e.st.R, out[i].ID)
		for _, s := range steps {
			line := fmt.Sprintf("%d. %s [%s]", s.StepNo, truncate(s.Description, 120), s.State)
			if s.ReasonCode != nil {
				line += " " + *s.ReasonCode
			}
			out[i].Steps = append(out[i].Steps, line)
		}
	}
	return out
}

// ActOnRemedies starts R2 remedies that are proposed: it drafts the corrective
// skill and launches the re-test tasks. Only inside the improvement budget.
func (e *Engine) ActOnRemedies(ctx context.Context) {
	// R3 remedies that already have a drafted tool wait for the owner, not for another draft.
	rows, err := e.st.R.QueryContext(ctx, `select r.id, r.limitation_id, r.class from limitation_remedies r join limitations l on l.id = r.limitation_id
		where r.class in ('R2','R3','R4') and r.status='proposed' and l.status in ('remedy_proposed','diagnosing')
		and not exists (select 1 from l2_tools t where t.remedy_id = r.id)
		and not exists (select 1 from brain_variants b where b.remedy_id = r.id)`)
	if err != nil {
		return
	}
	type item struct{ remedy, lim, class string }
	var todo []item
	for rows.Next() {
		var it item
		rows.Scan(&it.remedy, &it.lim, &it.class)
		todo = append(todo, it)
	}
	rows.Close()
	for _, it := range todo {
		if !e.improvementAllowed(ctx) {
			return // budget, kill switch, or the owner has work waiting
		}
		start := e.startSkillRemedy
		switch it.class {
		case "R3":
			start = e.startToolRemedy
		case "R4":
			start = e.startBrainRemedy
		}
		if err := start(ctx, it.remedy, it.lim); err != nil {
			e.log.Warn("skill remedy", "limitation", it.lim, "err", err)
			e.st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "update limitation_remedies set status='escalated', description=description || ?, updated_at=? where id=?",
					" Could not draft the skill: "+truncate(err.Error(), 200)+" (owner action needed).", ids.NowMs(), it.remedy)
				if err == nil {
					_, err = tx.ExecContext(ctx, "update limitations set status='escalated', updated_at=? where id=?", ids.NowMs(), it.lim)
				}
				return err
			})
		}
	}
}

func (e *Engine) startSkillRemedy(ctx context.Context, remedyID, limID string) error {
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
	failed := e.exemplars(ctx, l.TaskClass, "failed", 5)
	done := e.exemplars(ctx, l.TaskClass, "completed", 3)
	if len(failed) == 0 {
		return fmt.Errorf("no failed exemplars of class %q to learn from", l.TaskClass)
	}
	it, err := e.improvementTask(ctx, "skill remedy for "+limID, anyPrivate(failed, done) || l.DetectedBy == "owner_feedback")
	if err != nil {
		return err
	}
	var cause error
	defer func() { e.finishImprovementTask(ctx, it.ID, cause) }()
	s, err := e.newSession(ctx, it)
	if err != nil {
		cause = err
		return err
	}
	render := func(xs []exemplar) []map[string]any {
		var out []map[string]any
		for _, x := range xs {
			out = append(out, map[string]any{"objective": truncate(x.Objective, 400), "outcome": x.State, "reason_code": x.Code, "steps": x.Steps})
		}
		return out
	}
	raw, err := e.callWorker(ctx, s, "task.remedy_skill", map[string]any{"task_class": l.TaskClass, "limitation": l.Title,
		"diagnosis": l.Diagnosis, "failed": render(failed), "succeeded": render(done)})
	if err != nil {
		cause = err
		return err
	}
	var r reflection
	var wrap struct {
		Skill *struct {
			Name     string        `json:"name"`
			Triggers []string      `json:"triggers"`
			Steps    []plannedStep `json:"steps"`
			Notes    string        `json:"notes"`
		} `json:"skill"`
	}
	if err = json.Unmarshal(raw, &wrap); err != nil || wrap.Skill == nil {
		cause = fmt.Errorf("no skill proposed")
		return cause
	}
	_ = r
	prev := 0
	name := strings.ToLower(reSkillName.Replace(strings.TrimSpace(wrap.Skill.Name)))
	if ex, gerr := e.skillByName(ctx, name); gerr == nil {
		prev = ex.CurrentVersion
	}
	sk, err := e.saveSkill(ctx, it, wrap.Skill.Name, wrap.Skill.Triggers, wrap.Skill.Steps, wrap.Skill.Notes)
	if err != nil {
		cause = err
		return err
	}
	// Re-test fixtures: the failed exemplars (the exposing tasks) and a regression sample.
	var fixtures []map[string]any
	create := func(xs []exemplar, kind string) error {
		for _, x := range xs {
			t, _, err := e.submitRetest(ctx, x)
			if err != nil {
				return err
			}
			fixtures = append(fixtures, map[string]any{"task_id": t.ID, "kind": kind, "from": x.ID})
		}
		return nil
	}
	nFail := min(len(failed), max(1, e.ledgerCfg.RetestRuns-len(done)))
	if err = create(failed[:nFail], "exposing"); err == nil {
		err = create(done[:min(len(done), 2)], "regression")
	}
	if err != nil {
		cause = err
		return err
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		art := map[string]any{"skill_id": sk.ID, "skill": sk.Name, "version": sk.CurrentVersion, "previous_version": prev, "fixtures": fixtures}
		if _, err := tx.ExecContext(ctx, "update limitation_remedies set status='testing', artifact_json=?, before_json=?, updated_at=? where id=?",
			js(art), js(l.Baseline), ids.NowMs(), remedyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update limitations set status='remedy_testing', updated_at=? where id=?", ids.NowMs(), limID); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "REMEDY_STARTED", "core", "", "", map[string]any{"limitation": limID, "class": "R2", "skill": sk.Name, "version": sk.CurrentVersion})
	})
}

func (e *Engine) skillByName(ctx context.Context, name string) (*Skill, error) {
	row := e.st.R.QueryRowContext(ctx, skillQuery+" where s.name=?", name)
	return scanSkill(row)
}

// submitRetest re-runs an exemplar as a low-priority task with the original scope.
func (e *Engine) submitRetest(ctx context.Context, x exemplar, extraTools ...string) (*task.Task, bool, error) {
	tools := append(append([]string{}, x.Scope.Tools...), extraTools...)
	// The owner's attachments are copied from the original task's workspace.
	files := map[string]string{}
	for _, p := range x.Scope.Attachments {
		f, err := e.ws.Read(x.ID, p)
		if err != nil {
			return nil, false, fmt.Errorf("exemplar %s: attachment %s: %v", x.ID, p, err)
		}
		files[p] = f.Content
	}
	t, err := e.validate(CreateInput{Objective: x.Objective, Tools: tools, AllowDomains: x.Scope.AllowDomains, DataClass: x.Scope.DataClass,
		Budget: x.Budget, Attachments: files})
	if err != nil {
		return nil, false, err
	}
	t.AgentRole, t.Priority = "retest", -50
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := task.Insert(ctx, tx, t); err != nil {
			return err
		}
		for p, c := range files {
			if _, err := e.ws.Write(t.ID, p, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	e.Wake()
	return t, true, nil
}

// EvaluateRemedies judges R2 remedies whose re-test tasks all finished: promote
// if the defining metric improved beyond the noise margin with no regression;
// otherwise roll the skill back and record why.
func (e *Engine) EvaluateRemedies(ctx context.Context) {
	rows, err := e.st.R.QueryContext(ctx, `select r.id, r.limitation_id, r.artifact_json, r.before_json from limitation_remedies r
		where r.status='testing' and r.class in ('R2','R3')`)
	if err != nil {
		return
	}
	type item struct{ id, lim, art, before string }
	var todo []item
	for rows.Next() {
		var it item
		rows.Scan(&it.id, &it.lim, &it.art, &it.before)
		todo = append(todo, it)
	}
	rows.Close()
	for _, it := range todo {
		var art struct {
			ToolID          string `json:"tool_id"` // R3
			SkillID         string `json:"skill_id"`
			Version         int    `json:"version"`
			PreviousVersion int    `json:"previous_version"`
			Fixtures        []struct {
				TaskID string `json:"task_id"`
				Kind   string `json:"kind"`
			} `json:"fixtures"`
		}
		var before map[string]any
		json.Unmarshal([]byte(it.art), &art)
		json.Unmarshal([]byte(it.before), &before)
		var exposing, exposingOK, regress, regressOK int
		allDone := len(art.Fixtures) > 0
		for _, f := range art.Fixtures {
			t, err := task.Get(ctx, e.st.R, f.TaskID)
			if err != nil || !t.State.Terminal() {
				allDone = false
				continue
			}
			var verified int
			e.st.R.QueryRowContext(ctx, "select count(*) from task_events where task_id=? and type='VERIFIED'", f.TaskID).Scan(&verified)
			ok := t.State == task.Succeeded && verified > 0
			if f.Kind == "exposing" {
				exposing++
				if ok {
					exposingOK++
				}
			} else {
				regress++
				if ok {
					regressOK++
				}
			}
		}
		if !allDone {
			continue
		}
		baseline := floatOr(before["value"], 0)
		after := float64(exposingOK) / float64(max(exposing, 1))
		improved := after-baseline > e.ledgerCfg.NoiseMargin && exposingOK > 0
		regressed := regress > 0 && regressOK < regress
		after2 := map[string]any{"metric": "verified_success_rate_on_exposing_tasks", "value": after, "exposing_runs": exposing,
			"exposing_ok": exposingOK, "regression_runs": regress, "regression_ok": regressOK, "baseline": baseline, "noise_margin": e.ledgerCfg.NoiseMargin}
		e.st.Write(ctx, func(tx *sql.Tx) error {
			now := ids.NowMs()
			if improved && !regressed {
				tx.ExecContext(ctx, "update limitation_remedies set status='promoted', after_json=?, updated_at=? where id=?", js(after2), now, it.id)
				tx.ExecContext(ctx, "update limitations set status='resolved', result_json=?, closed_at=?, version=version+1, updated_at=? where id=?", js(after2), now, now, it.lim)
				return policy.Audit(ctx, tx, "REMEDY_PROMOTED", "core", "", "", map[string]any{"limitation": it.lim, "after": after2})
			}
			why := "did not beat the baseline beyond the noise margin"
			if regressed {
				why = "caused a regression on previously successful tasks"
			}
			after2["rollback_reason"] = why
			tx.ExecContext(ctx, "update limitation_remedies set status='rolled_back', after_json=?, updated_at=? where id=?", js(after2), now, it.id)
			tx.ExecContext(ctx, "update limitations set status='rolled_back', result_json=?, version=version+1, updated_at=? where id=?", js(after2), now, it.lim)
			return policy.Audit(ctx, tx, "REMEDY_ROLLED_BACK", "core", "", "", map[string]any{"limitation": it.lim, "reason": why})
		})
		if !(improved && !regressed) && art.ToolID != "" {
			e.DisableL2(ctx, art.ToolID, "core", "rolled back: "+fmt.Sprint(after2["rollback_reason"]))
			continue
		}
		if !(improved && !regressed) {
			if art.PreviousVersion > 0 {
				e.RollbackSkill(ctx, art.SkillID, art.PreviousVersion)
			} else {
				e.SetSkillStatus(ctx, art.SkillID, "disabled")
			}
			e.proposeBrainRemedy(ctx, it.lim) // the next rung for a planning-strategy limitation (R4, P6)
		}
	}
}

var _ = taxonomy.R2
