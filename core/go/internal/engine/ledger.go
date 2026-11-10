package engine

// Closed-loop self-improvement (Blueprint §8A.5): Detect → Record → Diagnose →
// Choose remedy class → Act within that class's gate → Re-test → Promote or
// roll back → Close with evidence. The Core owns the Limitation Ledger. A
// limitation opens only from evidence with a minimum sample (safety events
// open at once); a single failure never opens one; model self-report never
// opens or closes one. A permission boundary is never remedied by widening
// permissions: it becomes an owner request (R6).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/taxonomy"
)

// LedgerConfig holds the detection thresholds (Blueprint: configurable, with
// defaults) and the improvement budget.
type LedgerConfig struct {
	MinClassRuns        int     // runs of a task class before a success-rate limitation may open (default 10)
	SuccessRateTarget   float64 // verified success rate below this opens a limitation (default 0.70)
	MinRepeatedFailures int     // same canonical reason code this many times opens one (default 3)
	LookbackDays        int     // evidence window (default 14)
	RetestRuns          int     // tasks of the class after a remedy before it is judged (default 5)
	NoiseMargin         float64 // improvement must exceed this many points beyond baseline to promote (default 0.10)
	ImprovementUSDDaily float64 // separate daily cap for improvement work (default 0.25)
}

// DefaultLedgerConfig returns the Blueprint defaults.
func DefaultLedgerConfig() LedgerConfig {
	return LedgerConfig{MinClassRuns: 10, SuccessRateTarget: 0.70, MinRepeatedFailures: 3, LookbackDays: 14, RetestRuns: 5,
		NoiseMargin: 0.10, ImprovementUSDDaily: 0.25}
}

// Limitation is one ledger entry.
type Limitation struct {
	ID         string         `json:"id"`
	Version    int            `json:"version"`
	Status     string         `json:"status"`
	Category   string         `json:"category"`
	TaskClass  string         `json:"task_class"`
	Title      string         `json:"title"`
	DetectedBy string         `json:"detected_by"`
	Evidence   map[string]any `json:"evidence"`
	Impact     map[string]any `json:"impact"`
	Diagnosis  map[string]any `json:"diagnosis"`
	Baseline   map[string]any `json:"baseline"`
	Result     map[string]any `json:"result"`
	OwnerNote  string         `json:"owner_note"`
	Remedies   []Remedy       `json:"remedies,omitempty"`
	CreatedAt  int64          `json:"created_at"`
	UpdatedAt  int64          `json:"updated_at"`
	ClosedAt   *int64         `json:"closed_at"`
}

// Remedy is one remedy attempt linked to a limitation.
type Remedy struct {
	ID          string         `json:"id"`
	Class       string         `json:"class"`
	Version     int            `json:"version"`
	Status      string         `json:"status"`
	Description string         `json:"description"`
	Artifact    map[string]any `json:"artifact"`
	Before      map[string]any `json:"before"`
	After       map[string]any `json:"after"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
}

const limCols = `id, version, status, category, task_class, title, detected_by, evidence_json, impact_json, diagnosis_json,
	baseline_json, result_json, owner_note, created_at, updated_at, closed_at`

func scanLimitation(sc interface{ Scan(...any) error }) (*Limitation, error) {
	var l Limitation
	var ev, im, di, ba, re string
	if err := sc.Scan(&l.ID, &l.Version, &l.Status, &l.Category, &l.TaskClass, &l.Title, &l.DetectedBy, &ev, &im, &di, &ba, &re,
		&l.OwnerNote, &l.CreatedAt, &l.UpdatedAt, &l.ClosedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(ev), &l.Evidence)
	json.Unmarshal([]byte(im), &l.Impact)
	json.Unmarshal([]byte(di), &l.Diagnosis)
	json.Unmarshal([]byte(ba), &l.Baseline)
	json.Unmarshal([]byte(re), &l.Result)
	return &l, nil
}

func js(v any) string { b, _ := json.Marshal(v); return string(b) }

// reopenCooldown is how long a rolled-back limitation stays closed before the same weakness may reopen it.
const reopenCooldown = 24 * time.Hour

// openStatuses are the non-closed statuses.
const openStatuses = "('open','diagnosing','remedy_proposed','remedy_testing','escalated')"

// recordLimitation inserts a limitation unless an open one with the same dedupe
// key exists; it returns the existing/new entry and whether it was created.
func (e *Engine) recordLimitation(ctx context.Context, tx *sql.Tx, l *Limitation, key string) (*Limitation, bool, error) {
	// The key family is the key and its suffixed reopenings (key#2, key#3, ...): an open entry of the family is
	// the same limitation. Matching only the bare key reopened a new entry every cycle (2026-11-10 incident).
	family := "(dedupe_key = ? or dedupe_key like ?)"
	var id string
	if err := tx.QueryRowContext(ctx, "select id from limitations where "+family+" and status in "+openStatuses+" order by created_at desc limit 1",
		key, key+"#%").Scan(&id); err == nil {
		row, gerr := scanLimitation(tx.QueryRowContext(ctx, "select "+limCols+" from limitations where id=?", id))
		return row, false, gerr
	}
	// The owner said won't fix: never reopen. A remedy rolled back in the last 24 h: not again yet, so a weakness
	// that a remedy could not fix does not spend the improvement budget every cycle.
	// Only the most recently closed entry of the family decides (dismissing an old duplicate blocks nothing).
	var lastStatus string
	var lastAt int64
	tx.QueryRowContext(ctx, "select status, updated_at from limitations where "+family+" order by updated_at desc, created_at desc limit 1",
		key, key+"#%").Scan(&lastStatus, &lastAt)
	blocked := lastStatus == "wont_fix" || (lastStatus == "rolled_back" && lastAt > ids.NowMs()-reopenCooldown.Milliseconds())
	if blocked && l.DetectedBy != "owner_feedback" { // the owner can always add one
		return nil, false, nil
	}
	// A closed entry with the same key keeps its row (history); the new one gets a suffix.
	dk := key
	var prior int
	tx.QueryRowContext(ctx, "select count(*) from limitations where dedupe_key = ? or dedupe_key like ?", key, key+"#%").Scan(&prior)
	if prior > 0 {
		dk = fmt.Sprintf("%s#%d", key, prior+1)
	}
	now := ids.NowMs()
	l.ID, l.Version, l.CreatedAt, l.UpdatedAt = ids.New(), 1, now, now
	if l.Status == "" {
		l.Status = "open"
	}
	if l.Diagnosis == nil {
		l.Diagnosis = map[string]any{}
	}
	if l.Baseline == nil {
		l.Baseline = map[string]any{}
	}
	if l.Result == nil {
		l.Result = map[string]any{}
	}
	if l.Impact == nil {
		l.Impact = map[string]any{}
	}
	_, err := tx.ExecContext(ctx, `insert into limitations(id, version, status, category, task_class, title, detected_by, dedupe_key,
		evidence_json, impact_json, diagnosis_json, baseline_json, result_json, workspace_id, created_at, updated_at)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, l.ID, l.Version, l.Status, l.Category, l.TaskClass, l.Title, l.DetectedBy, dk,
		js(l.Evidence), js(l.Impact), js(l.Diagnosis), js(l.Baseline), js(l.Result), e.workspaceID(ctx), now, now)
	if err != nil {
		return nil, false, err
	}
	policy.Audit(ctx, tx, "LIMITATION_OPENED", "core", "", "", map[string]any{"id": l.ID, "category": l.Category, "task_class": l.TaskClass})
	return l, true, nil
}

// ---------------------------------------------------------------- detection

type classStat struct {
	Class      string
	Runs       int
	Completed  int
	Verified   int
	Failures   map[string]int
	FailedIDs  []string
	AvgSeconds float64
}

// classStats aggregates finished main tasks per task class inside the window.
func (e *Engine) classStats(ctx context.Context, since int64) (map[string]*classStat, error) {
	rows, err := e.st.R.QueryContext(ctx, `select t.id, coalesce(t.task_class,''), t.state, coalesce(t.reason_code,''),
		(select count(*) > 0 from task_events v where v.task_id = t.id and v.type='VERIFIED')
		or not exists (select 1 from task_events v where v.task_id = t.id and v.type in ('VERIFIED','VERIFICATION_FAILED','VERIFICATION_SKIPPED')),
		(t.updated_at - coalesce(t.started_at, t.created_at)) / 1000.0
		from tasks t where t.state in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED') and t.parent_task_id is null
		and t.agent_role = 'main' and t.created_at >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*classStat{}
	for rows.Next() {
		var id, class, state, code string
		var verified bool
		var secs float64
		rows.Scan(&id, &class, &state, &code, &verified, &secs)
		if state == "CANCELLED" {
			continue // an owner decision, not a limitation
		}
		s := out[class]
		if s == nil {
			s = &classStat{Class: class, Failures: map[string]int{}}
			out[class] = s
		}
		s.Runs++
		s.AvgSeconds += secs
		if state == "COMPLETED" {
			s.Completed++
			if verified {
				s.Verified++
			}
		} else {
			if code == "" {
				code = taxonomy.InternalError
			}
			s.Failures[code]++
			s.FailedIDs = append(s.FailedIDs, id)
		}
	}
	for _, s := range out {
		s.AvgSeconds /= float64(max(s.Runs, 1))
	}
	return out, rows.Err()
}

// Detect scans evidence and records limitations. It uses statistics only (no
// model). Returns the limitations opened by this scan.
func (e *Engine) Detect(ctx context.Context) ([]*Limitation, error) {
	cfg := e.ledgerCfg
	since := time.Now().Add(-time.Duration(cfg.LookbackDays) * 24 * time.Hour).UnixMilli()
	stats, err := e.classStats(ctx, since)
	if err != nil {
		return nil, err
	}
	var opened []*Limitation
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		classes := make([]string, 0, len(stats))
		for c := range stats {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		for _, c := range classes {
			s := stats[c]
			rate := float64(s.Verified) / float64(max(s.Runs, 1))
			// (1) verified success rate below target with enough runs
			if s.Runs >= cfg.MinClassRuns && rate < cfg.SuccessRateTarget {
				l, created, err := e.recordLimitation(ctx, tx, &Limitation{Category: "quality", TaskClass: c, DetectedBy: "self_model",
					Title: fmt.Sprintf("Verified success rate %.0f%% for %q is below the %.0f%% target", rate*100, c, cfg.SuccessRateTarget*100),
					Evidence: map[string]any{"runs": s.Runs, "verified": s.Verified, "completed": s.Completed, "failures_by_code": s.Failures,
						"failed_task_ids": firstN(s.FailedIDs, 10)},
					Impact:   map[string]any{"frequency": fmt.Sprintf("%d of %d runs", s.Runs-s.Verified, s.Runs), "avg_seconds": s.AvgSeconds},
					Baseline: map[string]any{"metric": "verified_success_rate", "value": rate, "runs": s.Runs, "target": cfg.SuccessRateTarget}},
					"rate:"+c)
				if err != nil {
					return err
				}
				if created {
					opened = append(opened, l)
				}
			}
			// (2) the same failure reason code repeating
			codes := make([]string, 0, len(s.Failures))
			for code := range s.Failures {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			for _, code := range codes {
				n := s.Failures[code]
				if n < cfg.MinRepeatedFailures {
					continue
				}
				l, created, err := e.recordLimitation(ctx, tx, &Limitation{Category: categoryOf(code), TaskClass: c, DetectedBy: "runtime_monitor",
					Title:    fmt.Sprintf("%s repeated %d times for %q", code, n, c),
					Evidence: map[string]any{"reason_code": code, "occurrences": n, "runs": s.Runs, "failed_task_ids": firstN(s.FailedIDs, 10)},
					Impact:   map[string]any{"frequency": fmt.Sprintf("%d of %d runs", n, s.Runs)},
					Baseline: map[string]any{"metric": "repeat_failure_count", "value": float64(n), "reason_code": code, "runs": s.Runs}},
					"code:"+c+":"+code)
				if err != nil {
					return err
				}
				if created {
					opened = append(opened, l)
				}
			}
		}
		// (3) safety events open immediately (no minimum sample) and are escalated.
		rows, err := tx.QueryContext(ctx, `select type, count(*), min(coalesce(task_id,'')) from audit_events
			where type in ('PROTECTED_PROPOSAL_REJECTED','CAPABILITY_DENIED') and at >= ? group by type`, since)
		if err != nil {
			return err
		}
		type safety struct {
			typ, task string
			n         int
		}
		var sev []safety
		for rows.Next() {
			var s safety
			rows.Scan(&s.typ, &s.n, &s.task)
			sev = append(sev, s)
		}
		rows.Close()
		for _, s := range sev {
			l, created, err := e.recordLimitation(ctx, tx, &Limitation{Category: "safety", DetectedBy: "runtime_monitor", Status: "escalated",
				Title:    fmt.Sprintf("Safety event: %s (%d occurrence(s)); the Core denied it, owner review requested", s.typ, s.n),
				Evidence: map[string]any{"audit_type": s.typ, "occurrences": s.n, "example_task_id": s.task},
				Impact:   map[string]any{"severity": "safety", "owner_visible": true},
				Diagnosis: map[string]any{"cause": taxonomy.CausePermissionBoundary, "confidence": 0.9, "method": "deterministic",
					"note": "the boundary held; it is never widened automatically"}}, "safety:"+s.typ)
			if err != nil {
				return err
			}
			if created {
				opened = append(opened, l)
				if err := e.addRemedy(ctx, tx, l.ID, taxonomy.R6, "escalated", "Owner review: confirm the denial was correct and decide whether the task scope should change.", nil); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return opened, err
}

func categoryOf(code string) string {
	switch code {
	case taxonomy.ProviderUnavailable:
		return "external_dependency"
	case taxonomy.PolicyDenied, taxonomy.OwnerRejected, taxonomy.ApprovalExpired:
		return "permission_boundary"
	case taxonomy.WorkerCrash, taxonomy.InternalError, taxonomy.ModelOutputInvalid:
		return "reliability"
	case taxonomy.BudgetExceeded, taxonomy.MaxIterations:
		return "cost"
	case taxonomy.MaxWallTime:
		return "latency"
	}
	return "quality"
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---------------------------------------------------------------- diagnosis

// Diagnose gives every open limitation a root cause: deterministic first
// (reason-code statistics); only when its confidence is low a model (standard
// tier) proposes a cause from the fixed taxonomy. Then the remedy class is
// chosen and the remedy is proposed or recorded, inside its class gate.
func (e *Engine) Diagnose(ctx context.Context) (int, error) {
	rows, err := e.st.R.QueryContext(ctx, "select "+limCols+" from limitations where status in ('open','diagnosing')")
	if err != nil {
		return 0, err
	}
	var todo []*Limitation
	for rows.Next() {
		if l, err := scanLimitation(rows); err == nil {
			todo = append(todo, l)
		}
	}
	rows.Close()
	n := 0
	for _, l := range todo {
		cause, conf, method := e.deterministicDiagnosis(l)
		if conf < 0.6 && e.improvementAllowed(ctx) {
			if c2, conf2, ok := e.modelDiagnosis(ctx, l); ok {
				cause, conf, method = c2, conf2, "model(standard)+statistics"
			}
		}
		class := taxonomy.RemedyClass(cause)
		diag := map[string]any{"cause": cause, "confidence": conf, "method": method, "remedy_class": class,
			"supporting_evidence": l.Evidence["failures_by_code"]}
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "update limitations set diagnosis_json=?, status='diagnosing', version=version+1, updated_at=? where id=? and status in ('open','diagnosing')",
				js(diag), ids.NowMs(), l.ID); err != nil {
				return err
			}
			return e.routeRemedy(ctx, tx, l, class)
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (e *Engine) deterministicDiagnosis(l *Limitation) (string, float64, string) {
	if c, ok := l.Diagnosis["cause"].(string); ok && taxonomy.ValidCause(c) {
		return c, floatOr(l.Diagnosis["confidence"], 0.5), "deterministic"
	}
	if code, ok := l.Evidence["reason_code"].(string); ok {
		c, f := taxonomy.CandidateCause(code)
		return c, f, "deterministic"
	}
	if l.Category == "capability_gap" {
		return taxonomy.CauseMissingTool, 0.6, "deterministic" // a capability the agent lacks: a tool (R3) is the first remedy
	}
	if by, ok := l.Evidence["failures_by_code"].(map[string]any); ok && len(by) > 0 {
		topCode, top, total := "", 0.0, 0.0
		for k, v := range by {
			n, _ := v.(float64)
			total += n
			if n > top || (n == top && k < topCode) {
				topCode, top = k, n
			}
		}
		c, f := taxonomy.CandidateCause(topCode)
		if total > 0 {
			f *= 0.5 + 0.5*top/total // a dominant failure code supports the diagnosis
		}
		return c, f, "deterministic"
	}
	return taxonomy.CausePlanStrategy, 0.3, "deterministic"
}

// modelDiagnosis asks the worker (standard tier) for a root cause from the
// fixed taxonomy. The answer is only accepted if it is in the taxonomy.
func (e *Engine) modelDiagnosis(ctx context.Context, l *Limitation) (string, float64, bool) {
	t, err := e.improvementTask(ctx, "diagnose "+l.ID, l.DetectedBy == "owner_feedback")
	if err != nil {
		return "", 0, false
	}
	defer e.finishImprovementTask(ctx, t.ID, nil)
	s, err := e.newSession(ctx, t)
	if err != nil {
		return "", 0, false
	}
	raw, err := e.callWorker(ctx, s, "task.diagnose", map[string]any{"title": l.Title, "category": l.Category, "task_class": l.TaskClass,
		"evidence": l.Evidence, "causes": taxonomy.Causes})
	if err != nil {
		return "", 0, false
	}
	var r struct {
		Cause      string  `json:"cause"`
		Confidence float64 `json:"confidence"`
	}
	if json.Unmarshal(raw, &r) != nil || !taxonomy.ValidCause(r.Cause) {
		return "", 0, false
	}
	return r.Cause, min(max(r.Confidence, 0), 1), true
}

// routeRemedy records the remedy for a diagnosed limitation inside its class gate.
func (e *Engine) routeRemedy(ctx context.Context, tx *sql.Tx, l *Limitation, class string) error {
	var existing int
	tx.QueryRowContext(ctx, "select count(*) from limitation_remedies where limitation_id=? and status in ('proposed','testing','escalated','recorded')", l.ID).Scan(&existing)
	if existing > 0 {
		return nil
	}
	set := func(status, limStatus, desc string, art map[string]any) error {
		if err := e.addRemedy(ctx, tx, l.ID, class, status, desc, art); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "update limitations set status=?, updated_at=? where id=?", limStatus, ids.NowMs(), l.ID)
		return err
	}
	switch class {
	case taxonomy.R0:
		return set("recorded", "remedy_proposed", "Runtime self-healing (provider failover, circuit breaker, retry) already acts on this; it is recorded and monitored. If it persists, the owner is asked (R6).", nil)
	case taxonomy.R1:
		return set("proposed", "remedy_proposed", "Routing/config change proposed to the owner (R1 stays manual until shadow-mode routing is promoted, P3b); evidence: /routing/shadow and /self-model.", nil)
	case taxonomy.R2:
		return set("proposed", "remedy_proposed", "A corrective skill (R2, L1) will be drafted from this class's successful and failed tasks and re-tested on fixtures.", map[string]any{"task_class": l.TaskClass})
	case taxonomy.R3:
		if e.sbx == nil {
			return set("escalated", "escalated", "A missing tool needs an L2 tool remedy, but the WASM sandbox is not configured (l2.python_wasm); owner request (R6).", nil)
		}
		return set("proposed", "remedy_proposed", "An L2 tool (R3) will be written, statically checked, tested and benchmarked in the WASM sandbox, then registered only after the owner approves its exact source.", map[string]any{"task_class": l.TaskClass})
	case taxonomy.R5:
		return set("escalated", "escalated", "A suspected defect in AURA's Core is a proposal only (R5): the owner decides; the fix is implemented by Claude Code under review.", nil)
	}
	return set("escalated", "escalated", "Owner action needed (R6): only the owner can resolve this (credential, budget, permission, knowledge source or scope decision). Permissions are never widened automatically.", nil)
}

func (e *Engine) addRemedy(ctx context.Context, tx *sql.Tx, limID, class, status, desc string, artifact map[string]any) error {
	now := ids.NowMs()
	var v int
	tx.QueryRowContext(ctx, "select coalesce(max(version),0)+1 from limitation_remedies where limitation_id=?", limID).Scan(&v)
	if artifact == nil {
		artifact = map[string]any{}
	}
	_, err := tx.ExecContext(ctx, `insert into limitation_remedies(id, limitation_id, class, version, status, description, artifact_json, created_at, updated_at)
		values(?,?,?,?,?,?,?,?,?)`, ids.New(), limID, class, v, status, truncate(desc, 600), js(artifact), now, now)
	return err
}

// ---------------------------------------------------------------- improvement budget

// improvementSpentToday is the improvement spend inside the workspace day.
func (e *Engine) improvementSpentToday(ctx context.Context) float64 {
	n := time.Now().In(e.cfg.Location())
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, e.cfg.Location()).UnixMilli()
	var s float64
	e.st.R.QueryRowContext(ctx, `select coalesce(sum(coalesce(u.cost_actual_usd, u.cost_reserved_usd)),0) from usage_records u
		join tasks t on t.id = u.task_id where t.agent_role in ('improvement','retest','lab') and u.created_at >= ?`, start).Scan(&s)
	return s
}

// improvementCallsToday counts today's model calls made for improvement work (any price).
func (e *Engine) improvementCallsToday(ctx context.Context) int {
	n := time.Now().In(e.cfg.Location())
	start := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, e.cfg.Location()).UnixMilli()
	var c int
	e.st.R.QueryRowContext(ctx, `select count(*) from usage_records u join tasks t on t.id = u.task_id
		where t.agent_role in ('improvement','retest','lab') and u.created_at >= ?`, start).Scan(&c)
	return c
}

// improvementAllowed reports whether improvement work may run now: inside its own
// daily cap, not halted by the kill switch, and only when no owner task is runnable
// (lowest priority: it never delays the owner's work).
func (e *Engine) improvementAllowed(ctx context.Context) bool {
	if e.gov.Halted() || e.improvementSpentToday(ctx) >= e.ledgerCfg.ImprovementUSDDaily {
		return false
	}
	if max := e.cfg.Improvement.DailyModelCalls; max > 0 && e.improvementCallsToday(ctx) >= max {
		return false
	}
	t, err := task.Runnable(ctx, e.st.R)
	return err == nil && t == nil
}

func (e *Engine) diagBudget() task.Budget {
	return task.Budget{MaxCostUSD: min(0.05, e.ledgerCfg.ImprovementUSDDaily), MaxIterations: 3, MaxWallSeconds: 120}
}

// improvementTask creates the housekeeping task that carries improvement spend
// (agent_role improvement), so it is accounted separately from owner tasks.
// private: its inputs include the owner's private tasks or notes, so its model
// calls must not use routes that may log prompts.
func (e *Engine) improvementTask(ctx context.Context, what string, private bool) (*task.Task, error) {
	dc := "public"
	if private {
		dc = "private"
	}
	t := &task.Task{Objective: "[improvement] " + what, AgentRole: "improvement", Budget: e.diagBudget(), ConfigHash: e.cfg.Hash(),
		Priority: -100, Scope: task.Scope{Tools: []string{}, DataClass: dc}}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := task.Insert(ctx, tx, t); err != nil {
			return err
		}
		if _, err := task.Transition(ctx, tx, t.ID, task.Planning, "", "", "core"); err != nil {
			return err
		}
		_, err := task.Transition(ctx, tx, t.ID, task.Running, "", "", "core")
		return err
	})
	if err != nil {
		return nil, err
	}
	return task.Get(ctx, e.st.R, t.ID)
}

func (e *Engine) finishImprovementTask(ctx context.Context, id string, cause error) {
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		to, reason := task.Succeeded, ""
		if cause != nil {
			to, reason = task.Failed, cause.Error()
		}
		if _, err := task.Transition(ctx, tx, id, to, reason, "", "core"); err != nil {
			return err
		}
		return policy.RevokeTask(ctx, tx, id)
	})
}

// ---------------------------------------------------------------- owner view

// ListLimitations returns ledger entries, open first.
func (e *Engine) ListLimitations(ctx context.Context, status string) ([]*Limitation, error) {
	q := "select " + limCols + " from limitations"
	var args []any
	if status == "open" {
		q += " where status in " + openStatuses
	} else if status != "" {
		q += " where status = ?"
		args = append(args, status)
	}
	rows, err := e.st.R.QueryContext(ctx, q+" order by (status in ('resolved','rolled_back','wont_fix')), updated_at desc limit 200", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Limitation{}
	for rows.Next() {
		l, err := scanLimitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	for _, l := range out {
		l.Remedies, _ = e.remediesOf(ctx, l.ID)
	}
	return out, nil
}

func (e *Engine) remediesOf(ctx context.Context, id string) ([]Remedy, error) {
	rows, err := e.st.R.QueryContext(ctx, `select id, class, version, status, description, artifact_json, before_json, after_json, created_at, updated_at
		from limitation_remedies where limitation_id=? order by version`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Remedy
	for rows.Next() {
		var r Remedy
		var a, b, f string
		rows.Scan(&r.ID, &r.Class, &r.Version, &r.Status, &r.Description, &a, &b, &f, &r.CreatedAt, &r.UpdatedAt)
		json.Unmarshal([]byte(a), &r.Artifact)
		json.Unmarshal([]byte(b), &r.Before)
		json.Unmarshal([]byte(f), &r.After)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddLimitation lets the owner record a limitation (detected_by owner_feedback).
func (e *Engine) AddLimitation(ctx context.Context, category, taskClass, title, note string) (*Limitation, error) {
	title = strings.TrimSpace(title)
	valid := map[string]bool{"capability_gap": true, "quality": true, "cost": true, "latency": true, "reliability": true, "resource": true,
		"knowledge": true, "safety": true, "external_dependency": true, "permission_boundary": true}
	if title == "" || len(title) > 300 || !valid[category] {
		return nil, fmt.Errorf("%w: title (1..300 chars) and a valid category are required", ErrInvalid)
	}
	var out *Limitation
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		l, _, err := e.recordLimitation(ctx, tx, &Limitation{Category: category, TaskClass: taskClass, Title: title, DetectedBy: "owner_feedback",
			Evidence: map[string]any{"owner_note": truncate(note, 1000)}, Impact: map[string]any{"source": "owner"}}, "owner:"+category+":"+taskClass+":"+title)
		out = l
		return err
	})
	return out, err
}

// LimitationAction applies an owner decision: dismiss or accept (wont_fix), or reopen.
func (e *Engine) LimitationAction(ctx context.Context, id, action, note string) (*Limitation, error) {
	status := map[string]string{"dismiss": "wont_fix", "accept": "wont_fix", "reopen": "open"}[action]
	if status == "" {
		return nil, fmt.Errorf("%w: action must be dismiss, accept or reopen", ErrInvalid)
	}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		now := ids.NowMs()
		var closed any
		if status == "wont_fix" {
			closed = now
		}
		res, err := tx.ExecContext(ctx, "update limitations set status=?, owner_note=?, version=version+1, updated_at=?, closed_at=? where id=?",
			status, truncate(note, 1000), now, closed, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return policy.Audit(ctx, tx, "LIMITATION_"+strings.ToUpper(action), "owner", "", "", map[string]any{"id": id})
	})
	if err != nil {
		return nil, err
	}
	return scanLimitation(e.st.R.QueryRowContext(ctx, "select "+limCols+" from limitations where id=?", id))
}

// OpenLimitationsDisclosure is the honest-disclosure text (Blueprint 8A.5):
// open limitations with their measured rates in plain words, computed without a model.
func (e *Engine) OpenLimitationsDisclosure(ctx context.Context) []string {
	ls, err := e.ListLimitations(ctx, "open")
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range ls {
		line := l.Title + "."
		if l.Baseline["metric"] == "verified_success_rate" {
			line = fmt.Sprintf("I currently complete and verify about %.0f of 10 tasks of the kind %q (target %.0f of 10).",
				floatOr(l.Baseline["value"], 0)*10, l.TaskClass, floatOr(l.Baseline["target"], 0.7)*10)
		}
		switch l.Status {
		case "remedy_testing":
			line += " I am testing a fix."
		case "remedy_proposed":
			line += " A remedy is proposed."
		case "escalated":
			line += " This needs your decision."
		}
		out = append(out, line)
	}
	return out
}

func floatOr(v any, d float64) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return d
}

func ledgerFrom(c *config.Config) LedgerConfig {
	i := c.Improvement
	return LedgerConfig{MinClassRuns: i.MinClassRuns, SuccessRateTarget: i.SuccessRateTarget, MinRepeatedFailures: i.MinRepeatedFailures,
		LookbackDays: i.LookbackDays, RetestRuns: i.RetestRuns, NoiseMargin: i.NoiseMargin, ImprovementUSDDaily: i.DailyCostUSD}
}

// Backfill gives tasks created before canonical reason codes and task classes existed
// (P1/P2 builds) their code and class, so the ledger measures them consistently.
// It is idempotent and only fills NULLs.
func (e *Engine) Backfill(ctx context.Context) {
	rows, err := e.st.R.QueryContext(ctx, "select id, coalesce(reason,''), scope_json, reason_code is null, task_class is null from tasks where (reason_code is null and reason is not null and reason != '') or task_class is null")
	if err != nil {
		return
	}
	type row struct {
		id, reason, scope   string
		needCode, needClass bool
	}
	var todo []row
	for rows.Next() {
		var r row
		rows.Scan(&r.id, &r.reason, &r.scope, &r.needCode, &r.needClass)
		todo = append(todo, r)
	}
	rows.Close()
	if len(todo) == 0 {
		return
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		for _, r := range todo {
			if r.needCode && r.reason != "" {
				tx.ExecContext(ctx, "update tasks set reason_code=? where id=? and reason_code is null", taxonomy.Canonical(r.reason), r.id)
			}
			if r.needClass {
				var sc task.Scope
				json.Unmarshal([]byte(r.scope), &sc)
				tx.ExecContext(ctx, "update tasks set task_class=? where id=? and task_class is null", task.ClassOf(sc), r.id)
			}
		}
		return nil
	})
	e.log.Info("backfilled reason codes and task classes", "tasks", len(todo))
}

// CloseRecovered is the monitoring half of the loop ("close with evidence"): an open
// statistical limitation is closed when the most recent MinClassRuns finished runs of its
// class no longer show the problem (success rate back at target, or the failure code gone).
// Entries whose class no longer exists (evidence re-attributed by a backfill) are closed as
// superseded. Owner, safety and in-test entries are never closed this way.
func (e *Engine) CloseRecovered(ctx context.Context) int {
	rows, err := e.st.R.QueryContext(ctx, "select "+limCols+", dedupe_key from limitations where status in ('open','diagnosing','remedy_proposed','escalated') and detected_by in ('self_model','runtime_monitor') and category != 'safety'")
	if err != nil {
		return 0
	}
	type item struct {
		l   *Limitation
		key string
	}
	var open []item
	for rows.Next() {
		var l Limitation
		var ev, im, di, ba, re, key string
		if err := rows.Scan(&l.ID, &l.Version, &l.Status, &l.Category, &l.TaskClass, &l.Title, &l.DetectedBy, &ev, &im, &di, &ba, &re,
			&l.OwnerNote, &l.CreatedAt, &l.UpdatedAt, &l.ClosedAt, &key); err == nil {
			json.Unmarshal([]byte(ba), &l.Baseline)
			json.Unmarshal([]byte(ev), &l.Evidence)
			open = append(open, item{&l, key})
		}
	}
	rows.Close()
	n := 0
	for _, it := range open {
		l := it.l
		var total int
		e.st.R.QueryRowContext(ctx, "select count(*) from tasks where coalesce(task_class,'') = ? and parent_task_id is null and agent_role='main'", l.TaskClass).Scan(&total)
		var result map[string]any
		if total == 0 {
			result = map[string]any{"closed_as": "superseded", "note": "no finished tasks carry this task class any more (evidence was re-attributed)"}
		} else {
			recent, err := e.recentRuns(ctx, l.TaskClass, e.ledgerCfg.MinClassRuns)
			if err != nil || len(recent) < e.ledgerCfg.MinClassRuns {
				continue
			}
			ok, bad := 0, 0
			code, _ := l.Evidence["reason_code"].(string)
			for _, r := range recent {
				if r.success {
					ok++
				}
				if code != "" && r.code == code {
					bad++
				}
			}
			rate := float64(ok) / float64(len(recent))
			switch {
			case strings.HasPrefix(it.key, "rate:") && rate >= e.ledgerCfg.SuccessRateTarget:
				result = map[string]any{"closed_as": "recovered", "recent_runs": len(recent), "verified_success_rate": rate, "target": e.ledgerCfg.SuccessRateTarget}
			case strings.HasPrefix(it.key, "code:") && bad == 0:
				result = map[string]any{"closed_as": "recovered", "recent_runs": len(recent), "occurrences_of_code": 0, "reason_code": code}
			default:
				continue
			}
		}
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			now := ids.NowMs()
			if _, err := tx.ExecContext(ctx, "update limitations set status='resolved', result_json=?, closed_at=?, version=version+1, updated_at=? where id=? and status in ('open','diagnosing','remedy_proposed','escalated')",
				js(result), now, now, l.ID); err != nil {
				return err
			}
			return policy.Audit(ctx, tx, "LIMITATION_CLOSED_BY_EVIDENCE", "core", "", "", map[string]any{"id": l.ID, "result": result})
		})
		if err == nil {
			n++
		}
	}
	return n
}

type runOutcome struct {
	success bool
	code    string
}

// recentRuns returns the latest finished main tasks of a class (newest first).
func (e *Engine) recentRuns(ctx context.Context, class string, limit int) ([]runOutcome, error) {
	rows, err := e.st.R.QueryContext(ctx, `select t.state, coalesce(t.reason_code,''),
		(select count(*) > 0 from task_events v where v.task_id = t.id and v.type='VERIFIED')
		or not exists (select 1 from task_events v where v.task_id = t.id and v.type in ('VERIFIED','VERIFICATION_FAILED','VERIFICATION_SKIPPED'))
		from tasks t where coalesce(t.task_class,'') = ? and t.parent_task_id is null and t.agent_role='main'
		and t.state in ('COMPLETED','FAILED','BUDGET_EXCEEDED') order by t.created_at desc limit ?`, class, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runOutcome
	for rows.Next() {
		var state, code string
		var verified bool
		rows.Scan(&state, &code, &verified)
		out = append(out, runOutcome{success: state == "COMPLETED" && verified, code: code})
	}
	return out, rows.Err()
}
