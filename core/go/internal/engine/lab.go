package engine

// L3 brain evolution through AURA Lab (Blueprint D.1 P6, §8A.1 L3, §8A.5 R4,
// ADR-0048). An R4 remedy drafts a *variant*: guidance for one reasoning module
// (planner, replanner or verifier) that the worker adds to that module's
// instructions. The variant is evaluated in AURA Lab against the current
// version on the tasks that exposed the limitation plus a regression sample
// (paired baseline/variant runs); only a variant that beats the baseline with
// no safety regression is offered to the owner, whose approval is bound to the
// variant's sha256. An approved variant first serves a canary share of new
// tasks; it is promoted when the canary does at least as well as the control
// group, and rolled back automatically otherwise. The owner can roll back at
// any time.
//
// Boundaries: a variant is text guidance for a module, never code, tools,
// permissions or policy; the Core enforces grants, tiers and approvals the same
// way whatever the guidance says. Lab tasks use only read/compute tools and
// workspace writes, can never request an approval, spend the improvement
// budget, and their workspaces are deleted after evaluation.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/taxonomy"
)

// ErrLabDisabled means AURA Lab (lab.enabled) is off.
var ErrLabDisabled = errors.New("lab_disabled")

var labTaskIDRe = regexp.MustCompile(`^[0-9A-Za-z]{10,40}$`)

var brainModules = []string{"planner", "replanner", "verifier"}

// labTools are the tools a Lab task may use: reads, computation and workspace
// writes. Exemplars that needed anything else are not replayed in the Lab.
var labTools = []string{"web.fetch", "web.search", "data.read", "calc", "workspace.read", "workspace.write"}

// BrainVariant is one L3 variant.
type BrainVariant struct {
	ID              string         `json:"id"`
	RemedyID        *string        `json:"remedy_id"`
	LimitationID    *string        `json:"limitation_id"`
	Module          string         `json:"module"`
	Guidance        string         `json:"guidance"`
	Rationale       string         `json:"rationale"`
	SHA256          string         `json:"sha256"`
	Status          string         `json:"status"`
	Lab             map[string]any `json:"lab"`
	Rollout         map[string]any `json:"rollout"`
	DecidedBy       *string        `json:"decided_by"`
	DecidedAt       *int64         `json:"decided_at"`
	CanaryStartedAt *int64         `json:"canary_started_at"`
	CreatedAt       int64          `json:"created_at"`
	UpdatedAt       int64          `json:"updated_at"`
}

const brainCols = `id, remedy_id, limitation_id, module, guidance, rationale, sha256, status, lab_json, rollout_json,
	decided_by, decided_at, canary_started_at, created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanBrain(r rowScanner) (*BrainVariant, error) {
	var v BrainVariant
	var lab, ro string
	if err := r.Scan(&v.ID, &v.RemedyID, &v.LimitationID, &v.Module, &v.Guidance, &v.Rationale, &v.SHA256, &v.Status, &lab, &ro,
		&v.DecidedBy, &v.DecidedAt, &v.CanaryStartedAt, &v.CreatedAt, &v.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, task.ErrNotFound
		}
		return nil, err
	}
	json.Unmarshal([]byte(lab), &v.Lab)
	json.Unmarshal([]byte(ro), &v.Rollout)
	return &v, nil
}

// ListBrainVariants lists variants, newest first (status "" = all).
func (e *Engine) ListBrainVariants(ctx context.Context, status string) ([]*BrainVariant, error) {
	q, args := "select "+brainCols+" from brain_variants", []any{}
	if status != "" {
		q += " where status = ?"
		args = append(args, status)
	}
	rows, err := e.st.R.QueryContext(ctx, q+" order by created_at desc limit 200", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BrainVariant
	for rows.Next() {
		v, err := scanBrain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetBrainVariant returns one variant.
func (e *Engine) GetBrainVariant(ctx context.Context, id string) (*BrainVariant, error) {
	return scanBrain(e.st.R.QueryRowContext(ctx, "select "+brainCols+" from brain_variants where id=?", id))
}

func guidanceHash(module, guidance string) string {
	h := sha256.Sum256([]byte(module + "\n" + guidance))
	return hex.EncodeToString(h[:])
}

// canaryMember decides deterministically whether a task gets a canary variant.
func (e *Engine) canaryMember(taskID, variantID string) bool {
	h := fnv.New32a()
	h.Write([]byte(variantID + "/" + taskID))
	return int(h.Sum32()%100) < e.cfg.Lab.CanaryPercent
}

// brainGuidance returns the module guidance a task's worker calls carry and the
// variants it came from. Active variants apply to every task; a canary variant
// replaces its module's active one for the canary share of tasks created after
// its rollout began; a Lab task gets exactly its arm's version.
func (e *Engine) brainGuidance(ctx context.Context, t *task.Task) (map[string]string, []string) {
	rows, err := e.st.R.QueryContext(ctx, "select "+brainCols+" from brain_variants where status in ('active','canary') order by created_at")
	if err != nil {
		return nil, nil
	}
	var vs []*BrainVariant
	for rows.Next() {
		if v, err := scanBrain(rows); err == nil {
			vs = append(vs, v)
		}
	}
	rows.Close()
	g, from := map[string]string{}, map[string]string{}
	for _, v := range vs {
		if v.Status == "active" {
			g[v.Module], from[v.Module] = v.Guidance, v.ID
		}
	}
	if t.AgentRole == "lab" {
		var vid, arm string
		if e.st.R.QueryRowContext(ctx, "select variant_id, arm from lab_runs where task_id=?", t.ID).Scan(&vid, &arm) == nil && arm == "variant" {
			if v, err := e.GetBrainVariant(ctx, vid); err == nil {
				g[v.Module], from[v.Module] = v.Guidance, v.ID
			}
		}
	} else {
		for _, v := range vs {
			if v.Status == "canary" && v.CanaryStartedAt != nil && t.CreatedAt >= *v.CanaryStartedAt && e.canaryMember(t.RootTaskID, v.ID) {
				g[v.Module], from[v.Module] = v.Guidance, v.ID
			}
		}
	}
	if len(g) == 0 {
		return nil, nil
	}
	var used []string
	for _, m := range brainModules {
		if id, ok := from[m]; ok {
			used = append(used, id)
		}
	}
	return g, used
}

// labEligible reports whether an exemplar can be replayed in the Lab.
func labEligible(x exemplar) bool {
	if len(x.Scope.Tools) == 0 || len(x.Scope.Attachments) > 0 {
		return false
	}
	for _, t := range x.Scope.Tools {
		if !slices.Contains(labTools, t) {
			return false
		}
	}
	return true
}

func eligible(xs []exemplar) []exemplar {
	var out []exemplar
	for _, x := range xs {
		if labEligible(x) {
			out = append(out, x)
		}
	}
	return out
}

func anyPrivate(xs ...[]exemplar) bool {
	for _, l := range xs {
		for _, x := range l {
			if !x.Scope.IsPublic() {
				return true
			}
		}
	}
	return false
}

// startBrainRemedy acts on an R4 remedy: the worker drafts a variant from the
// limitation and its tasks, then paired baseline/variant Lab runs start.
func (e *Engine) startBrainRemedy(ctx context.Context, remedyID, limID string) error {
	if !e.cfg.Lab.Enabled {
		return ErrLabDisabled
	}
	l, err := e.limitation(ctx, limID)
	if err != nil {
		return err
	}
	failed := eligible(e.exemplars(ctx, l.TaskClass, "failed", 10))
	done := eligible(e.exemplars(ctx, l.TaskClass, "completed", 6))
	failed, done = failed[:min(len(failed), 3)], done[:min(len(done), 2)]
	if len(failed) == 0 {
		return fmt.Errorf("no failed tasks of class %q that the Lab can replay (read/compute tools only)", l.TaskClass)
	}
	it, err := e.improvementTask(ctx, "L3 brain variant for "+limID, anyPrivate(failed, done) || l.DetectedBy == "owner_feedback")
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
	current, _ := e.brainGuidance(ctx, &task.Task{})
	raw, err := e.callWorker(ctx, s, "task.draft_brain_variant", map[string]any{"limitation": l.Title, "task_class": l.TaskClass,
		"diagnosis": l.Diagnosis, "failed": render(failed), "succeeded": render(done), "modules": brainModules, "current": current})
	if err != nil {
		cause = err
		return err
	}
	var d struct {
		Module    string `json:"module"`
		Guidance  string `json:"guidance"`
		Rationale string `json:"rationale"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		cause = fmt.Errorf("invalid variant draft: %v", err)
		return cause
	}
	d.Guidance, d.Rationale = strings.TrimSpace(d.Guidance), truncate(strings.TrimSpace(d.Rationale), 600)
	if p := checkGuidance(d.Module, d.Guidance); p != "" {
		cause = fmt.Errorf("variant draft refused: %s", p)
		return cause
	}
	v := &BrainVariant{ID: ids.New(), Module: d.Module, Guidance: d.Guidance, Rationale: d.Rationale, SHA256: guidanceHash(d.Module, d.Guidance)}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		now := ids.NowMs()
		if _, err := tx.ExecContext(ctx, `insert into brain_variants(id, remedy_id, limitation_id, module, guidance, rationale, sha256, status,
			created_at, updated_at) values(?,?,?,?,?,?,?,'lab_testing',?,?)`, v.ID, remedyID, limID, v.Module, v.Guidance, v.Rationale, v.SHA256, now, now); err != nil {
			return err
		}
		runs := 0
		for _, set := range []struct {
			xs   []exemplar
			kind string
		}{{failed, "exposing"}, {done, "regression"}} {
			for _, x := range set.xs {
				for _, arm := range []string{"baseline", "variant"} {
					if err := e.submitLab(ctx, tx, x, v.ID, arm, set.kind); err != nil {
						return err
					}
					runs++
				}
			}
		}
		art := map[string]any{"variant_id": v.ID, "module": v.Module, "sha256": v.SHA256, "lab_runs": runs}
		if _, err := tx.ExecContext(ctx, "update limitation_remedies set status='testing', artifact_json=?, before_json=?, updated_at=? where id=?",
			js(art), js(l.Baseline), now, remedyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update limitations set status='remedy_testing', updated_at=? where id=?", now, limID); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "REMEDY_STARTED", "core", "", "", map[string]any{"limitation": limID, "class": "R4", "variant": v.ID,
			"module": v.Module, "sha256": v.SHA256, "lab_runs": runs})
	})
}

// checkGuidance is the Core's gate on a drafted variant.
func checkGuidance(module, g string) string {
	switch {
	case !slices.Contains(brainModules, module):
		return "module must be planner, replanner or verifier"
	case len(g) < 20 || len(g) > 1500:
		return "guidance must be 20..1500 characters"
	case strings.Contains(g, "```") || strings.Contains(strings.ToLower(g), "untrusted_observation") || strings.Contains(g, "<tool_call"):
		return "guidance must be plain prose (no code blocks or markup)"
	}
	return ""
}

func (e *Engine) limitation(ctx context.Context, id string) (*Limitation, error) {
	ls, err := e.ListLimitations(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, x := range ls {
		if x.ID == id {
			return x, nil
		}
	}
	return nil, task.ErrNotFound
}

// submitLab creates one Lab task: the exemplar's objective and scope, a capped
// budget, the lowest priority above housekeeping, and its arm recorded.
func (e *Engine) submitLab(ctx context.Context, tx *sql.Tx, x exemplar, variantID, arm, kind string) error {
	b := x.Budget
	b.MaxCostUSD = min(b.MaxCostUSD, e.cfg.Lab.MaxTaskCostUSD)
	t, err := e.validate(CreateInput{Objective: x.Objective, Tools: x.Scope.Tools, AllowDomains: x.Scope.AllowDomains, DataClass: x.Scope.DataClass, Reply: "none", Budget: b})
	if err != nil {
		return err
	}
	t.AgentRole, t.Priority = "lab", -60
	if err := task.Insert(ctx, tx, t); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "insert into lab_runs(task_id, variant_id, arm, kind, from_task_id) values(?,?,?,?,?)", t.ID, variantID, arm, kind, x.ID)
	if err == nil {
		defer e.Wake()
	}
	return err
}

type armResult struct {
	Runs, OK, ExposingRuns, ExposingOK, RegressionRuns, RegressionOK, Denials int
	CostUSD                                                                   float64
}

func (e *Engine) taskOK(ctx context.Context, t *task.Task) bool {
	var verified int
	e.st.R.QueryRowContext(ctx, "select count(*) from task_events where task_id=? and type='VERIFIED'", t.ID).Scan(&verified)
	return t.State == task.Succeeded && verified > 0
}

func (e *Engine) denials(ctx context.Context, taskID string) int {
	var n int
	e.st.R.QueryRowContext(ctx, "select count(*) from audit_events where type='CAPABILITY_DENIED' and task_id=?", taskID).Scan(&n)
	return n
}

// EvaluateLab judges variants whose Lab runs all finished, then cleans the Lab
// up. A variant passes only if it beats the baseline overall, is not worse on
// either the exposing or the regression tasks, and has no more capability
// denials than the baseline (no safety regression).
func (e *Engine) EvaluateLab(ctx context.Context) {
	vs, err := e.ListBrainVariants(ctx, "lab_testing")
	if err != nil {
		return
	}
	for _, v := range vs {
		rows, err := e.st.R.QueryContext(ctx, "select task_id, arm, kind from lab_runs where variant_id=?", v.ID)
		if err != nil {
			continue
		}
		type run struct{ id, arm, kind string }
		var runs []run
		for rows.Next() {
			var r run
			rows.Scan(&r.id, &r.arm, &r.kind)
			runs = append(runs, r)
		}
		rows.Close()
		res := map[string]*armResult{"baseline": {}, "variant": {}}
		allDone := len(runs) > 0
		for _, r := range runs {
			t, err := task.Get(ctx, e.st.R, r.id)
			if err != nil || !t.State.Terminal() {
				allDone = false
				break
			}
			a, ok := res[r.arm], e.taskOK(ctx, t)
			a.Runs++
			a.Denials += e.denials(ctx, t.ID)
			a.CostUSD += t.CostUSD
			if ok {
				a.OK++
			}
			if r.kind == "exposing" {
				a.ExposingRuns++
				if ok {
					a.ExposingOK++
				}
			} else {
				a.RegressionRuns++
				if ok {
					a.RegressionOK++
				}
			}
		}
		if !allDone {
			continue
		}
		b, c := res["baseline"], res["variant"]
		why := ""
		switch {
		case c.Denials > b.Denials:
			why = "safety regression: more capability denials than the current version"
		case c.RegressionOK < b.RegressionOK:
			why = "regression on previously successful tasks"
		case c.ExposingOK < b.ExposingOK:
			why = "worse on the tasks that exposed the limitation"
		case c.OK <= b.OK:
			why = "did not beat the current version"
		}
		report := map[string]any{"baseline": b, "variant": c, "passed": why == "", "reason": why}
		e.cleanLab(ctx, v.ID)
		e.st.Write(ctx, func(tx *sql.Tx) error {
			now := ids.NowMs()
			if why == "" {
				if _, err := tx.ExecContext(ctx, "update brain_variants set status='awaiting_approval', lab_json=?, updated_at=? where id=?", js(report), now, v.ID); err != nil {
					return err
				}
				return policy.Audit(ctx, tx, "LAB_PASSED", "core", "", "", map[string]any{"variant": v.ID, "module": v.Module, "report": report})
			}
			if _, err := tx.ExecContext(ctx, "update brain_variants set status='lab_failed', lab_json=?, updated_at=? where id=?", js(report), now, v.ID); err != nil {
				return err
			}
			e.closeBrainRemedy(ctx, tx, v, "rolled_back", report)
			return policy.Audit(ctx, tx, "LAB_FAILED", "core", "", "", map[string]any{"variant": v.ID, "reason": why})
		})
	}
}

// cleanLab deletes the Lab tasks' workspaces (resource cleanup is part of the
// Lab contract); the task records stay for the audit trail.
func (e *Engine) cleanLab(ctx context.Context, variantID string) {
	rows, err := e.st.R.QueryContext(ctx, "select task_id from lab_runs where variant_id=? and cleaned_at is null", variantID)
	if err != nil {
		return
	}
	var tids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		tids = append(tids, id)
	}
	rows.Close()
	for _, id := range tids {
		if !labTaskIDRe.MatchString(id) { // never let a stored value reach outside the workspace root
			continue
		}
		if err := os.RemoveAll(filepath.Join(e.ws.Root(), id)); err != nil {
			e.log.Warn("lab cleanup", "task", id, "err", err)
			continue
		}
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "update lab_runs set cleaned_at=? where task_id=?", ids.NowMs(), id)
			return err
		})
	}
}

// closeBrainRemedy records the outcome on the remedy and its limitation.
func (e *Engine) closeBrainRemedy(ctx context.Context, tx *sql.Tx, v *BrainVariant, status string, after map[string]any) {
	if v.RemedyID == nil {
		return
	}
	now := ids.NowMs()
	tx.ExecContext(ctx, "update limitation_remedies set status=?, after_json=?, updated_at=? where id=?", status, js(after), now, *v.RemedyID)
	lim := "rolled_back"
	if status == "promoted" {
		lim = "resolved"
	}
	if status == "escalated" {
		lim = "escalated"
	}
	if lim == "resolved" {
		tx.ExecContext(ctx, "update limitations set status=?, result_json=?, closed_at=?, version=version+1, updated_at=? where id=?", lim, js(after), now, now, deref(v.LimitationID))
		return
	}
	tx.ExecContext(ctx, "update limitations set status=?, result_json=?, version=version+1, updated_at=? where id=?", lim, js(after), now, deref(v.LimitationID))
}

// DecideBrain is the owner's decision on a variant that passed the Lab. An
// approval is bound to the variant's sha256 and starts the canary rollout.
func (e *Engine) DecideBrain(ctx context.Context, id string, approve bool, hash, by string) (*BrainVariant, error) {
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		v, err := scanBrain(tx.QueryRowContext(ctx, "select "+brainCols+" from brain_variants where id=?", id))
		if err != nil {
			return err
		}
		if v.Status != "awaiting_approval" {
			return fmt.Errorf("%w: variant is %s, not awaiting approval", task.ErrInvalidTransition, v.Status)
		}
		now := ids.NowMs()
		if !approve {
			if _, err := tx.ExecContext(ctx, "update brain_variants set status='rejected', decided_by=?, decided_at=?, updated_at=? where id=?", by, now, now, id); err != nil {
				return err
			}
			e.closeBrainRemedy(ctx, tx, v, "escalated", map[string]any{"owner": "rejected the variant"})
			return policy.Audit(ctx, tx, "BRAIN_VARIANT_REJECTED", by, "", "", map[string]any{"variant": id})
		}
		if hash != v.SHA256 {
			return fmt.Errorf("%w: the approved hash does not match the variant", policy.ErrMismatch)
		}
		var canaries int
		tx.QueryRowContext(ctx, "select count(*) from brain_variants where module=? and status='canary'", v.Module).Scan(&canaries)
		if canaries > 0 {
			return fmt.Errorf("%w: another %s variant is already in canary rollout", task.ErrInvalidTransition, v.Module)
		}
		if _, err := tx.ExecContext(ctx, `update brain_variants set status='canary', decided_by=?, decided_at=?, canary_started_at=?, rollout_json=?,
			updated_at=? where id=?`, by, now, now, js(map[string]any{"canary_percent": e.cfg.Lab.CanaryPercent, "min_tasks": e.cfg.Lab.CanaryMinTasks}), now, id); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "BRAIN_VARIANT_APPROVED", by, "", "", map[string]any{"variant": id, "module": v.Module, "sha256": v.SHA256,
			"canary_percent": e.cfg.Lab.CanaryPercent})
	})
	if err != nil {
		return nil, err
	}
	return e.GetBrainVariant(ctx, id)
}

// RollbackBrain takes a canary or active variant out of service.
func (e *Engine) RollbackBrain(ctx context.Context, id, by, reason string) (*BrainVariant, error) {
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		v, err := scanBrain(tx.QueryRowContext(ctx, "select "+brainCols+" from brain_variants where id=?", id))
		if err != nil {
			return err
		}
		if v.Status != "canary" && v.Status != "active" {
			return fmt.Errorf("%w: variant is %s", task.ErrInvalidTransition, v.Status)
		}
		return e.rollbackBrainTx(ctx, tx, v, by, truncate(reason, 300), nil)
	})
	if err != nil {
		return nil, err
	}
	return e.GetBrainVariant(ctx, id)
}

func (e *Engine) rollbackBrainTx(ctx context.Context, tx *sql.Tx, v *BrainVariant, by, reason string, stats map[string]any) error {
	now := ids.NowMs()
	ro := v.Rollout
	if ro == nil {
		ro = map[string]any{}
	}
	ro["rolled_back_by"], ro["reason"] = by, reason
	if stats != nil {
		ro["canary"] = stats
	}
	if _, err := tx.ExecContext(ctx, "update brain_variants set status='rolled_back', rollout_json=?, updated_at=? where id=?", js(ro), now, v.ID); err != nil {
		return err
	}
	if v.Status == "canary" {
		e.closeBrainRemedy(ctx, tx, v, "rolled_back", ro)
	}
	return policy.Audit(ctx, tx, "BRAIN_VARIANT_ROLLED_BACK", by, "", "", map[string]any{"variant": v.ID, "module": v.Module, "reason": reason})
}

// EvaluateCanaries promotes or rolls back canary variants once enough canary
// tasks finished: rolled back when the canary's verified success rate is below
// the control group's by more than the noise margin, or its capability denials
// per task are higher (safety regression); otherwise promoted, replacing the
// module's previous active variant.
func (e *Engine) EvaluateCanaries(ctx context.Context) {
	vs, err := e.ListBrainVariants(ctx, "canary")
	if err != nil {
		return
	}
	for _, v := range vs {
		if v.CanaryStartedAt == nil {
			continue
		}
		rows, err := e.st.R.QueryContext(ctx, `select id from tasks where parent_task_id is null and agent_role='main' and created_at >= ?
			and state in ('COMPLETED','FAILED','BUDGET_EXCEEDED','CANCELLED') order by created_at`, *v.CanaryStartedAt)
		if err != nil {
			continue
		}
		var tids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			tids = append(tids, id)
		}
		rows.Close()
		var can, ctl armResult
		for _, id := range tids {
			t, err := task.Get(ctx, e.st.R, id)
			if err != nil || t.State == task.Cancelled {
				continue // a cancelled task says nothing about the variant
			}
			a := &ctl
			if e.canaryMember(t.ID, v.ID) {
				a = &can
			}
			a.Runs++
			a.Denials += e.denials(ctx, t.ID)
			if e.taskOK(ctx, t) {
				a.OK++
			}
		}
		if can.Runs < e.cfg.Lab.CanaryMinTasks {
			continue
		}
		rate := func(a armResult, n int) float64 { return float64(n) / float64(max(a.Runs, 1)) }
		ctlRate := rate(ctl, ctl.OK)
		if ctl.Runs == 0 { // no control tasks yet: compare with the Lab baseline
			if b, ok := v.Lab["baseline"].(map[string]any); ok {
				ctlRate = floatOr(b["OK"], 0) / max(floatOr(b["Runs"], 1), 1)
			}
		}
		stats := map[string]any{"canary_runs": can.Runs, "canary_ok": can.OK, "canary_rate": rate(can, can.OK), "control_runs": ctl.Runs,
			"control_ok": ctl.OK, "control_rate": ctlRate, "canary_denials": can.Denials, "control_denials": ctl.Denials,
			"noise_margin": e.ledgerCfg.NoiseMargin}
		why := ""
		if rate(can, can.OK) < ctlRate-e.ledgerCfg.NoiseMargin {
			why = "canary success rate fell below the control group"
		} else if rate(can, can.Denials) > rate(ctl, ctl.Denials) {
			why = "canary had more capability denials per task than the control group (safety regression)"
		}
		e.st.Write(ctx, func(tx *sql.Tx) error {
			if why != "" {
				return e.rollbackBrainTx(ctx, tx, v, "core", why, stats)
			}
			now := ids.NowMs()
			if _, err := tx.ExecContext(ctx, "update brain_variants set status='superseded', updated_at=? where module=? and status='active'", now, v.Module); err != nil {
				return err
			}
			ro := v.Rollout
			if ro == nil {
				ro = map[string]any{}
			}
			ro["canary"] = stats
			if _, err := tx.ExecContext(ctx, "update brain_variants set status='active', rollout_json=?, updated_at=? where id=?", js(ro), now, v.ID); err != nil {
				return err
			}
			e.closeBrainRemedy(ctx, tx, v, "promoted", stats)
			return policy.Audit(ctx, tx, "BRAIN_VARIANT_PROMOTED", "core", "", "", map[string]any{"variant": v.ID, "module": v.Module, "canary": stats})
		})
	}
}

// proposeBrainRemedy opens an R4 remedy after a skill (R2) failed to fix a
// planning-strategy limitation, when AURA Lab is enabled.
func (e *Engine) proposeBrainRemedy(ctx context.Context, limID string) bool {
	if !e.cfg.Lab.Enabled {
		return false
	}
	l, err := e.limitation(ctx, limID)
	if err != nil || l.Diagnosis["cause"] != taxonomy.CausePlanStrategy {
		return false
	}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(ctx, "select count(*) from limitation_remedies where limitation_id=? and class='R4'", limID).Scan(&n)
		if n > 0 {
			return errors.New("already tried")
		}
		if err := e.addRemedy(ctx, tx, limID, taxonomy.R4, "proposed", "The corrective skill did not help, so a brain variant (R4, L3) will be drafted, "+
			"evaluated against the current version in AURA Lab, and offered to the owner only if it wins with no safety regression.", map[string]any{"task_class": l.TaskClass}); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "update limitations set status='remedy_proposed', updated_at=? where id=?", ids.NowMs(), limID)
		return err
	})
	return err == nil
}
