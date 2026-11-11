package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/taxonomy"
)

// seedTasks inserts finished main tasks of a class directly (a fixture of past
// traces): state, canonical reason code, and whether they were verified.
func seedTasks(t *testing.T, h *harness, class string, n int, state task.State, reasonCode string, verified bool) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		tk := &task.Task{Objective: fmt.Sprintf("seeded %s %d", class, ids.NowMs()+int64(i)), ConfigHash: "h",
			Scope: task.Scope{Tools: strings.Split(class, "+"), DataClass: "public"}}
		err := h.st.Write(context.Background(), func(tx *sql.Tx) error {
			if err := task.Insert(context.Background(), tx, tk); err != nil {
				return err
			}
			if _, err := tx.Exec("update tasks set state=?, reason_code=?, task_class=?, updated_at=? where id=?", string(state),
				nullStr(reasonCode), class, ids.NowMs(), tk.ID); err != nil {
				return err
			}
			if verified {
				_, err := task.AppendEvent(context.Background(), tx, tk.ID, nil, "VERIFIED", "core", map[string]any{})
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tk.ID)
	}
	return out
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func limByKey(t *testing.T, h *harness, status string) []*Limitation {
	ls, err := h.eng.ListLimitations(context.Background(), status)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

// SELF-01: a seeded weakness opens a ledger entry only after the minimum sample;
// a single failure does not; a safety event opens immediately.
func TestSELF01Detection(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	const class = "web.fetch+workspace.write"

	seedTasks(t, h, class, 1, task.Failed, taxonomy.ToolFailed, false)
	seedTasks(t, h, class, 3, task.Succeeded, "", true)
	if opened, _ := h.eng.Detect(ctx); len(opened) != 0 {
		t.Fatalf("one failure (4 runs) opened %d limitations", len(opened))
	}
	seedTasks(t, h, class, 2, task.Failed, taxonomy.ToolFailed, false) // now 3 identical failures
	opened, err := h.eng.Detect(ctx)
	if err != nil || len(opened) != 1 || opened[0].Evidence["reason_code"] != taxonomy.ToolFailed || opened[0].DetectedBy != "runtime_monitor" {
		t.Fatalf("repeated failure code: %v %+v", err, opened)
	}
	if again, _ := h.eng.Detect(ctx); len(again) != 0 {
		t.Fatal("the same limitation was opened twice")
	}
	// success-rate limitation needs >= 10 runs
	seedTasks(t, h, class, 5, task.Failed, taxonomy.VerificationFailed, false)
	opened, _ = h.eng.Detect(ctx)
	var rate *Limitation
	for _, l := range opened {
		if l.Baseline["metric"] == "verified_success_rate" {
			rate = l
		}
	}
	if rate == nil || rate.Baseline["runs"].(int) < 10 {
		t.Fatalf("no success-rate limitation at >= 10 runs: %+v", opened)
	}
	// safety events open immediately, with no minimum sample, and are escalated
	h.st.Write(ctx, func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "PROTECTED_PROPOSAL_REJECTED", "worker", "", "", map[string]any{"kind": "skill"})
	})
	opened, _ = h.eng.Detect(ctx)
	if len(opened) != 1 || opened[0].Category != "safety" || opened[0].Status != "escalated" {
		t.Fatalf("safety event: %+v", opened)
	}
}

// SELF-02/03: diagnosis lands in the fixed taxonomy and each cause routes to the
// right remedy class; a permission boundary never becomes an automatic remedy.
func TestSELF02And03DiagnosisAndRemedyRouting(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	cases := []struct {
		code   string
		cause  string
		class  string
		status string
	}{
		{taxonomy.ProviderUnavailable, taxonomy.CauseExternalDependency, "R0", "remedy_proposed"},
		{taxonomy.PolicyDenied, taxonomy.CausePermissionBoundary, "R6", "escalated"},
		{taxonomy.ModelOutputInvalid, taxonomy.CauseWrongTierRoute, "R1", "remedy_proposed"},
		{taxonomy.PlanInvalid, taxonomy.CausePlanStrategy, "R2", "remedy_proposed"},
		{taxonomy.WorkerCrash, taxonomy.CauseCodeDefect, "R5", "escalated"},
	}
	correct := 0
	for i, c := range cases {
		class := fmt.Sprintf("calc+tool%d", i)
		seedTasks(t, h, class, 3, task.Failed, c.code, false)
	}
	h.eng.Detect(ctx)
	h.eng.ledgerCfg.ImprovementUSDDaily = 0 // diagnosis here is deterministic; no model help
	if _, err := h.eng.Diagnose(ctx); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		class := fmt.Sprintf("calc+tool%d", i)
		var l *Limitation
		for _, x := range limByKey(t, h, "") {
			if x.TaskClass == class {
				l = x
			}
		}
		if l == nil {
			t.Fatalf("%s: no limitation", c.code)
		}
		cause, _ := l.Diagnosis["cause"].(string)
		if !taxonomy.ValidCause(cause) {
			t.Fatalf("diagnosis outside the fixed taxonomy: %q", cause)
		}
		if cause == c.cause {
			correct++
		}
		if len(l.Remedies) != 1 || l.Remedies[0].Class != c.class || l.Status != c.status {
			t.Errorf("%s: remedy %+v status %s, want class %s status %s", c.code, l.Remedies, l.Status, c.class, c.status)
		}
		if c.cause == taxonomy.CausePermissionBoundary && (l.Remedies[0].Class != "R6" || strings.Contains(strings.ToLower(l.Remedies[0].Description), "widen the")) {
			t.Error("permission boundary must be an owner request")
		}
	}
	if float64(correct)/float64(len(cases)) < 0.8 {
		t.Fatalf("diagnosis accuracy %d/%d below 80%%", correct, len(cases))
	}
}

// R2 loop (SELF-04): a corrective skill is drafted, re-tested on the exposing
// tasks plus a regression sample, and promoted only when the metric improves
// beyond noise; otherwise rolled back with the reason recorded.
func TestSELF04RetestPromoteAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		works   bool
		wantLim string
		wantRem string
	}{{"remedy works", true, "resolved", "promoted"}, {"remedy does not help", false, "rolled_back", "rolled_back"}} {
		t.Run(tc.name, func(t *testing.T) {
			skillSeen := false
			h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
				user := req.Messages[len(req.Messages)-1].Content
				switch {
				case isSys(req, "skill-remedy module"):
					b, _ := json.Marshal(map[string]any{"skill": map[string]any{"name": "fetch-then-write", "triggers": []string{"fetch", "pages", "summary"},
						"steps": []any{gatewaytest.Step(1, "fetch each page", "web.fetch", map[string]any{"url": "<url>"}),
							gatewaytest.Step(2, "write", "workspace.write", map[string]any{"path": "<path>"})},
						"notes": "Fetch only the pages named in the objective; do not fetch pages that are not mentioned."}})
					return gatewaytest.Reply{Content: string(b)}
				case isSys(req, "planning module") && !isSys(req, "replanning module"):
					skillSeen = skillSeen || strings.Contains(user, "Proven approaches")
					steps := []map[string]any{}
					n := 1
					if !(tc.works && strings.Contains(user, "Proven approaches")) {
						// the weak plan: fetch a page that does not exist, then (maybe) nothing useful
						steps = append(steps, gatewaytest.Step(n, "fetch http://fixture.test/missing", "web.fetch", map[string]any{"url": "http://fixture.test/missing"}))
						n++
						steps = append(steps, gatewaytest.Step(n, "write summary", "workspace.write", map[string]any{"path": "reports/summary.md"}))
						return planWith([]string{"the summary file summarizes each page with its source link"}, steps...)
					}
					steps = append(steps, gatewaytest.Step(n, "fetch http://fixture.test/one", "web.fetch", map[string]any{"url": "http://fixture.test/one"}))
					n++
					steps = append(steps, gatewaytest.Step(n, "write summary", "workspace.write", map[string]any{"path": "reports/summary.md"}))
					return planWith([]string{"the summary file summarizes each page with its source link"}, steps...)
				case isSys(req, "replanning module"):
					// replans cannot rescue the weak plan: the remedy has to come from the skill
					return planWith([]string{}, gatewaytest.Step(2, "write summary", "workspace.write", map[string]any{"path": "reports/summary.md"}))
				case isSys(req, "reflection module"):
					return gatewaytest.Reply{Content: `{"lesson":null,"fact":null,"skill":null,"preference":null}`}
				}
				return goodModel(r, req)
			}})
			ctx := context.Background()
			h.eng.ledgerCfg.RetestRuns = 3
			const class = "web.fetch+workspace.write"
			// Past traces of the weak behaviour: real (finished) tasks with objectives to re-run.
			for i := 0; i < 8; i++ {
				seedFromReal(t, h, class, "Fetch http://fixture.test/missing and summarize into reports/summary.md", task.Failed, taxonomy.VerificationFailed, false)
			}
			for i := 0; i < 2; i++ {
				seedFromReal(t, h, class, "Fetch http://fixture.test/one and summarize into reports/summary.md", task.Succeeded, "", true)
			}
			opened, _ := h.eng.Detect(ctx)
			if len(opened) == 0 {
				t.Fatal("nothing detected")
			}
			h.eng.ledgerCfg.ImprovementUSDDaily = 1
			if _, err := h.eng.Diagnose(ctx); err != nil {
				t.Fatal(err)
			}
			h.eng.ActOnRemedies(ctx)
			var lim *Limitation
			for _, l := range limByKey(t, h, "remedy_testing") {
				if l.Baseline["metric"] == "verified_success_rate" {
					lim = l
				}
			}
			if lim == nil {
				t.Fatalf("remedy not started: %+v", limByKey(t, h, ""))
			}
			if lim.Remedies[0].Class != "R2" || lim.Remedies[0].Status != "testing" {
				t.Fatalf("%+v", lim.Remedies)
			}
			// the re-test tasks run at the lowest priority, as 'retest' tasks
			var retests int
			h.st.R.QueryRow("select count(*) from tasks where agent_role='retest' and priority < 0").Scan(&retests)
			if retests < 3 {
				t.Fatalf("re-test tasks: %d", retests)
			}
			deadline := time.Now().Add(40 * time.Second)
			for time.Now().Before(deadline) {
				h.eng.EvaluateRemedies(ctx)
				if ls := limByKey(t, h, tc.wantLim); len(ls) > 0 {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			var final *Limitation
			for _, l := range limByKey(t, h, tc.wantLim) {
				if l.Baseline["metric"] == "verified_success_rate" {
					final = l
				}
			}
			if final == nil {
				t.Fatalf("expected %s; ledger: %s", tc.wantLim, dumpLedger(t, h))
			}
			rem := final.Remedies[0]
			if rem.Status != tc.wantRem || rem.After["metric"] == nil || rem.Before["value"] == nil {
				t.Fatalf("remedy %+v", rem)
			}
			if !tc.works && rem.After["rollback_reason"] == nil {
				t.Fatal("rollback reason not recorded")
			}
			// skill state follows the verdict
			sk, _ := h.eng.skillByName(ctx, "fetch-then-write")
			if tc.works && (sk == nil || sk.Status != "active") {
				t.Fatalf("promoted skill not active: %+v", sk)
			}
			if !tc.works && (sk == nil || sk.Status != "disabled") {
				t.Fatalf("rolled-back skill still active: %+v", sk)
			}
		})
	}
}

func dumpLedger(t *testing.T, h *harness) string {
	b, _ := json.Marshal(limByKey(t, h, ""))
	return string(b)
}

// seedFromReal inserts a finished main task with a real objective/scope (so a re-test can re-run it).
func seedFromReal(t *testing.T, h *harness, class, objective string, state task.State, code string, verified bool) string {
	t.Helper()
	tk := &task.Task{Objective: objective, ConfigHash: "h", Budget: task.Budget{MaxCostUSD: 0.5, MaxIterations: 20, MaxWallSeconds: 300},
		Scope: task.Scope{Tools: []string{"web.fetch", "workspace.write"}, AllowDomains: []string{h.host}, DataClass: "public"}}
	err := h.st.Write(context.Background(), func(tx *sql.Tx) error {
		if err := task.Insert(context.Background(), tx, tk); err != nil {
			return err
		}
		if _, err := tx.Exec("update tasks set state=?, reason_code=?, task_class=?, updated_at=? where id=?", string(state), nullStr(code), class, ids.NowMs(), tk.ID); err != nil {
			return err
		}
		if verified {
			_, err := task.AppendEvent(context.Background(), tx, tk.ID, nil, "VERIFIED", "core", map[string]any{})
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tk.ID
}

// SELF-05: improvement work has its own cap, runs only when the owner has no
// runnable task, and stops with the kill switch.
func TestSELF05ImprovementBudget(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.eng.ledgerCfg.ImprovementUSDDaily = 0.05
	if !h.eng.improvementAllowed(ctx) {
		t.Fatal("improvement should be allowed when idle and under the cap")
	}
	// spend beyond the cap on an improvement task
	it, err := h.eng.improvementTask(ctx, "cap test", false)
	if err != nil {
		t.Fatal(err)
	}
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`insert into usage_records(id, task_id, tier, provider_id, api_model_id, route_id, cost_estimated_usd, cost_reserved_usd,
			cost_actual_usd, outcome, purpose, created_at) values('U1',?,'standard','x','m','r',0.06,0.06,0.06,'ok','diagnose',?)`, it.ID, ids.NowMs())
		return err
	})
	if h.eng.improvementAllowed(ctx) {
		t.Fatal("improvement allowed beyond its own daily cap")
	}
	h.eng.ledgerCfg.ImprovementUSDDaily = 5
	if !h.eng.improvementAllowed(ctx) {
		t.Fatal("cap not the limiting factor")
	}
	// an owner task waiting means improvement yields
	h.eng.gov.Halt() // kill switch
	if h.eng.improvementAllowed(ctx) {
		t.Fatal("improvement ran while the kill switch was active")
	}
	h.eng.gov.Resume()
	owner := &task.Task{Objective: "owner work", ConfigHash: "h", Scope: task.Scope{Tools: []string{"calc"}}}
	h.st.Write(ctx, func(tx *sql.Tx) error { return task.Insert(ctx, tx, owner) })
	h.eng.Cancel(ctx, owner.ID) // keep the scheduler from running it during the check
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update tasks set state='CREATED' where id=?", owner.ID)
		return err
	})
	h.stop()
	<-h.done
	h.stop = nil
	if h.eng.improvementAllowed(ctx) {
		t.Fatal("improvement ran while an owner task was runnable")
	}
	// improvement tasks are accounted separately and are lowest priority
	var prio int
	h.st.R.QueryRow("select priority from tasks where id=?", it.ID).Scan(&prio)
	if prio >= 0 {
		t.Fatal("improvement task not lowest priority")
	}
}

// SELF-06: the self-model discloses open limitations with their measured rates.
func TestSELF06HonestDisclosure(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	const class = "web.fetch+workspace.write"
	seedTasks(t, h, class, 4, task.Succeeded, "", true)
	seedTasks(t, h, class, 6, task.Failed, taxonomy.VerificationFailed, false)
	h.eng.Detect(ctx)
	lines := h.eng.OpenLimitationsDisclosure(ctx)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "about 4 of 10 tasks") || !strings.Contains(joined, class) {
		t.Fatalf("disclosure lacks the measured rate:\n%s", joined)
	}
	sm, err := h.eng.SelfModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	open, _ := sm["open_limitations"].([]string)
	if len(open) == 0 || !strings.Contains(strings.Join(open, " "), "4 of 10") {
		t.Fatalf("self-model omitted open limitations: %v", sm["open_limitations"])
	}
	// the owner can accept (wont_fix) a limitation: it leaves the disclosure
	for _, l := range limByKey(t, h, "open") {
		h.eng.LimitationAction(ctx, l.ID, "accept", "known and accepted")
	}
	if left := h.eng.OpenLimitationsDisclosure(ctx); len(left) != 0 {
		t.Fatalf("accepted limitations still disclosed as open: %v", left)
	}
	if ls := limByKey(t, h, "wont_fix"); len(ls) == 0 || ls[0].OwnerNote == "" {
		t.Fatal("owner decision not recorded")
	}
}

// SELF-07 (engine part): after a forced Core stop in the middle of work, tasks
// recover automatically with no duplicate side effect — covered end to end by
// app.TestCrashRecovery (20 SIGKILLs); here the provider outage path: a dead
// first route fails over, the task completes, and the failure is visible as R0
// evidence without opening a limitation by itself.
func TestSELF07ProviderOutageSelfHeals(t *testing.T) {
	var primaryCalls int
	h := newHarness(t, harnessOpts{respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if r.RouteID == "fake/standard" {
			primaryCalls++
			return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 503, Transient: true, Msg: "outage"}}
		}
		return goodModel(r, req)
	}})
	// add a fallback route for the standard tier
	h.cfg.Models["standard"] = append(h.cfg.Models["standard"], config.ModelRoute{DisplayName: "S2", APIModelID: "s2", RouteID: "fake/standard-2",
		PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 4000})
	h.eng.gw = gateway.New(h.fake, h.cfg.Models, h.eng.gov)
	h.eng.gw.SetBackoff(func(int) time.Duration { return time.Millisecond })
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded || writesTo(h.events(tk.ID), "reports/summary.md") != 1 {
		t.Fatalf("%s writes=%d %s", done.State, writesTo(h.events(tk.ID), "reports/summary.md"), errorDetail(h.events(tk.ID)))
	}
	if primaryCalls == 0 {
		t.Fatal("primary route never tried")
	}
	if opened, _ := h.eng.Detect(context.Background()); len(opened) != 0 {
		t.Fatalf("a handled outage must not open a limitation by itself: %+v", opened)
	}
}

// A task that waits in the queue must not lose wall time before it starts.
func TestWallClockStartsWhenTaskStarts(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tk := &task.Task{Objective: rs1Objective(h.host), ConfigHash: "h", Budget: task.Budget{MaxCostUSD: 0.5, MaxIterations: 20, MaxWallSeconds: 30},
		Scope: task.Scope{Tools: []string{"web.fetch", "workspace.write"}, AllowDomains: []string{h.host}, DataClass: "public"}}
	h.eng.gov.Halt() // keep it queued
	h.st.Write(context.Background(), func(tx *sql.Tx) error {
		if err := task.Insert(context.Background(), tx, tk); err != nil {
			return err
		}
		// created 10 minutes ago: with creation-based deadlines its 30 s budget would already be gone
		_, err := tx.Exec("update tasks set created_at=? where id=?", time.Now().Add(-10*time.Minute).UnixMilli(), tk.ID)
		return err
	})
	h.eng.gov.Resume()
	h.eng.Wake()
	done := h.wait(tk.ID, task.Succeeded, task.Failed, task.BudgetExceeded)
	if done.State != task.Succeeded {
		t.Fatalf("queued task lost its wall budget: %s %s", done.State, deref(done.Reason))
	}
}

func TestBackfillGivesOldTasksCodesAndClasses(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	tk := &task.Task{Objective: "old", ConfigHash: "h", Scope: task.Scope{Tools: []string{"workspace.write", "web.fetch"}}}
	h.st.Write(ctx, func(tx *sql.Tx) error {
		if err := task.Insert(ctx, tx, tk); err != nil {
			return err
		}
		_, err := tx.Exec("update tasks set state='FAILED', reason='invalid_plan: x', reason_code=null, task_class=null where id=?", tk.ID)
		return err
	})
	h.eng.Backfill(ctx)
	got, _ := task.Get(ctx, h.st.R, tk.ID)
	if got.ReasonCode == nil || *got.ReasonCode != taxonomy.PlanInvalid || got.TaskClass == nil || *got.TaskClass != "web.fetch+workspace.write" {
		t.Fatalf("%v %v", got.ReasonCode, got.TaskClass)
	}
}

// Monitoring closes a limitation with evidence once recent runs no longer show it.
func TestLimitationClosedByEvidenceWhenRecovered(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	const class = "web.fetch+workspace.write"
	seedTasks(t, h, class, 4, task.Failed, taxonomy.ToolFailed, false)
	h.eng.Detect(ctx)
	if len(limByKey(t, h, "open")) == 0 {
		t.Fatal("not detected")
	}
	if n := h.eng.CloseRecovered(ctx); n != 0 {
		t.Fatal("closed without recovery evidence")
	}
	seedTasks(t, h, class, 10, task.Succeeded, "", true) // the 10 most recent runs are all good
	if n := h.eng.CloseRecovered(ctx); n == 0 {
		t.Fatal("recovered limitation was not closed")
	}
	ls := limByKey(t, h, "resolved")
	if len(ls) == 0 || ls[0].Result["closed_as"] != "recovered" || ls[0].ClosedAt == nil {
		t.Fatalf("%+v", ls)
	}
}

// 2026-11-10 incident: once a limitation of a key had been closed, every later
// reopening got a suffixed key and was never matched again, so each detection
// cycle opened a new entry (and a new remedy attempt). An open entry of the key
// family is the same limitation, and a rolled-back one waits 24 h.
func TestLimitationNotReopenedEveryCycle(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	const class = "calc+workspace.write"
	seedTasks(t, h, class, 3, task.Succeeded, "", true)
	seedTasks(t, h, class, 8, task.Failed, taxonomy.VerificationFailed, false)
	count := func() int {
		var n int
		h.st.R.QueryRow("select count(*) from limitations where task_class=?", class).Scan(&n)
		return n
	}
	h.eng.Detect(ctx)
	first := count()
	if first == 0 {
		t.Fatal("nothing detected")
	}
	// Close every entry as rolled back long ago: the next detection may reopen (with suffixed keys).
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update limitations set status='rolled_back', updated_at=? where task_class=?", ids.NowMs()-48*3600*1000, class)
		return err
	})
	h.eng.Detect(ctx)
	second := count()
	if second != 2*first {
		t.Fatalf("reopen after the cooldown: %d entries, want %d", second, 2*first)
	}
	// The reopened (suffixed) entries are found again: further cycles add nothing.
	for i := 0; i < 3; i++ {
		h.eng.Detect(ctx)
	}
	if n := count(); n != second {
		t.Fatalf("a new entry every cycle: %d, want %d", n, second)
	}
	// A fresh rollback blocks reopening for 24 h.
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update limitations set status='rolled_back', updated_at=? where task_class=? and status != 'rolled_back' or (task_class=? and updated_at > ?)",
			ids.NowMs(), class, class, ids.NowMs()-3600*1000)
		return err
	})
	h.eng.Detect(ctx)
	if n := count(); n != second {
		t.Fatalf("reopened within the cooldown: %d, want %d", n, second)
	}
}

// Free model calls cost $0, so improvement work also has a daily call cap.
func TestImprovementDailyCallCap(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	h.eng.ledgerCfg.ImprovementUSDDaily = 5
	h.eng.cfg.Improvement.DailyModelCalls = 2
	if !h.eng.improvementAllowed(ctx) {
		t.Fatal("improvement refused before any call")
	}
	it, err := h.eng.improvementTask(ctx, "cap", false)
	if err != nil {
		t.Fatal(err)
	}
	h.st.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 2; i++ {
			if _, err := tx.Exec(`insert into usage_records(id, task_id, tier, cost_estimated_usd, cost_reserved_usd, cost_actual_usd, outcome, created_at)
				values(?,?,?,0,0,0,'ok',?)`, ids.New(), it.ID, "fast", ids.NowMs()); err != nil {
				return err
			}
		}
		return nil
	})
	if h.eng.improvementAllowed(ctx) {
		t.Fatal("free calls were not counted against the daily cap")
	}
}

// 2026-11-10 churn: detection looked at the whole lookback window while closing looked at the
// most recent runs, so a recovered weakness was resolved and reopened every cycle (a new
// ledger entry and a new remedy attempt each time). Both now decide on the same evidence.
func TestRecoveredLimitationIsNotReopenedByOldFailures(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	const class = "web.fetch+workspace.write"
	seedTasks(t, h, class, 4, task.Failed, taxonomy.ToolFailed, false)
	h.eng.Detect(ctx)
	seedTasks(t, h, class, 10, task.Succeeded, "", true) // old failures stay inside the lookback window
	count := func() int {
		var n int
		h.st.R.QueryRow("select count(*) from limitations where task_class=?", class).Scan(&n)
		return n
	}
	first := count()
	if first == 0 {
		t.Fatal("nothing detected")
	}
	for i := 0; i < 3; i++ {
		h.eng.CloseRecovered(ctx)
		if opened, _ := h.eng.Detect(ctx); len(opened) != 0 {
			t.Fatalf("cycle %d reopened a recovered limitation: %+v", i, opened[0])
		}
	}
	if n := count(); n != first {
		t.Fatalf("ledger grew from %d to %d entries", first, n)
	}
	if len(limByKey(t, h, "open")) != 0 {
		t.Fatal("a recovered limitation is still open")
	}
	// The weakness coming back is new evidence: it opens again.
	seedTasks(t, h, class, 3, task.Failed, taxonomy.ToolFailed, false)
	if opened, _ := h.eng.Detect(ctx); len(opened) == 0 {
		t.Fatal("a returning weakness was not detected")
	}
}

// Improvement housekeeping interrupted by a Core restart is closed, not left RUNNING for ever.
func TestInterruptedImprovementTaskIsFailedOnRecovery(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	tk, err := h.eng.improvementTask(ctx, "diagnose x", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := task.Get(ctx, h.st.R, tk.ID)
	if got.State != task.Failed || deref(got.Reason) != "interrupted_improvement" {
		t.Fatalf("%s %s", got.State, deref(got.Reason))
	}
}

// seedRemedy opens a limitation with an R2 remedy in testing over the given fixtures.
func seedRemedy(t *testing.T, h *harness, class string, baseline map[string]any, fixtures []map[string]any) (limID, remedyID string) {
	t.Helper()
	ctx := context.Background()
	err := h.st.Write(ctx, func(tx *sql.Tx) error {
		l, _, err := h.eng.recordLimitation(ctx, tx, &Limitation{Category: "quality", TaskClass: class, DetectedBy: "runtime_monitor", Status: "remedy_testing",
			Title: "seeded", Baseline: baseline}, "code:"+class+":seeded")
		if err != nil {
			return err
		}
		limID = l.ID
		if err := h.eng.addRemedy(ctx, tx, l.ID, taxonomy.R2, "testing", "seeded", map[string]any{"skill_id": "none", "fixtures": fixtures}); err != nil {
			return err
		}
		if err := tx.QueryRow("select id from limitation_remedies where limitation_id=?", l.ID).Scan(&remedyID); err != nil {
			return err
		}
		_, err = tx.Exec("update limitation_remedies set before_json=? where id=?", js(baseline), remedyID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return limID, remedyID
}

func remedyRow(t *testing.T, h *harness, id string) (status string, art, after map[string]any) {
	t.Helper()
	var a, b string
	if err := h.st.R.QueryRow("select status, artifact_json, after_json from limitation_remedies where id=?", id).Scan(&status, &a, &b); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(a), &art)
	json.Unmarshal([]byte(b), &after)
	return status, art, after
}

const retestClass = "web.fetch+workspace.write"
const retestObjective = "Fetch http://fixture.test/one and summarize into reports/summary.md"

// P10-M5b: a re-test that ended because no provider was available says nothing about the remedy. It is run
// again and never counted as a regression; and a repeated-failure-code limitation is judged against a
// success-rate baseline (its failure count, 3, was compared with a rate and could never be beaten).
func TestRetestInfrastructureFailureIsRerunNotHeldAgainstTheRemedy(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	h.eng.ledgerCfg.ImprovementUSDDaily = 1
	old := retestBackoff
	retestBackoff = 0
	t.Cleanup(func() { retestBackoff = old })
	from := seedFromReal(t, h, retestClass, retestObjective, task.Succeeded, "", true)
	ok1 := seedFromReal(t, h, retestClass, retestObjective, task.Succeeded, "", true)
	ok2 := seedFromReal(t, h, retestClass, retestObjective, task.Succeeded, "", true)
	down := seedFromReal(t, h, retestClass, retestObjective, task.Failed, taxonomy.ProviderUnavailable, false)
	_, rem := seedRemedy(t, h, retestClass, map[string]any{"metric": "repeat_failure_count", "value": 3.0}, []map[string]any{
		{"task_id": ok1, "kind": "exposing", "from": from}, {"task_id": ok2, "kind": "exposing", "from": from},
		{"task_id": down, "kind": "regression", "from": from}})
	h.eng.EvaluateRemedies(ctx)
	status, art, _ := remedyRow(t, h, rem)
	fx := art["fixtures"].([]any)[2].(map[string]any)
	if status != "testing" || fx["task_id"] == down || fx["attempts"] != 2.0 {
		t.Fatalf("the inconclusive fixture was not run again: %s %+v", status, fx)
	}
	rerun, _ := task.Get(ctx, h.st.R, fx["task_id"].(string))
	if rerun.AgentRole != "retest" || rerun.Priority >= 0 {
		t.Fatalf("re-run is not a low-priority retest: %+v", rerun)
	}
	h.st.Write(ctx, func(tx *sql.Tx) error {
		tx.Exec("update tasks set state='COMPLETED' where id=?", rerun.ID)
		_, err := task.AppendEvent(ctx, tx, rerun.ID, nil, "VERIFIED", "core", map[string]any{})
		return err
	})
	h.eng.EvaluateRemedies(ctx)
	status, _, after := remedyRow(t, h, rem)
	if status != "promoted" || after["baseline"] != 0.0 || after["exposing_ok"] != 2.0 {
		t.Fatalf("%s %+v", status, after)
	}
}

// With no conclusive run at all the remedy is withdrawn as unproven, and the record says so.
func TestRetestWithoutEvidenceIsRecordedAsInconclusive(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	from := seedFromReal(t, h, retestClass, retestObjective, task.Succeeded, "", true)
	var fixtures []map[string]any
	for _, kind := range []string{"exposing", "exposing", "regression"} {
		id := seedFromReal(t, h, retestClass, retestObjective, task.Failed, taxonomy.ProviderUnavailable, false)
		fixtures = append(fixtures, map[string]any{"task_id": id, "kind": kind, "from": from, "attempts": retestMaxAttempts})
	}
	lim, rem := seedRemedy(t, h, retestClass, map[string]any{"metric": "verified_success_rate", "value": 0.5}, fixtures)
	h.eng.EvaluateRemedies(ctx)
	status, _, after := remedyRow(t, h, rem)
	why, _ := after["rollback_reason"].(string)
	if status != "rolled_back" || !strings.HasPrefix(why, "inconclusive") || after["inconclusive_runs"] != 3.0 || after["regression_runs"] != 0.0 {
		t.Fatalf("%s %+v", status, after)
	}
	var n int
	h.st.R.QueryRow("select count(*) from limitation_remedies where limitation_id=? and class='R4'", lim).Scan(&n)
	if n != 0 {
		t.Fatal("an unproven remedy led to a brain remedy")
	}
}

// Improvement work never asks the owner: a re-test waiting for an answer is ended and left out.
func TestWaitingRetestIsReleasedAndLeftOut(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	from := seedFromReal(t, h, retestClass, retestObjective, task.Succeeded, "", true)
	ok1 := seedFromReal(t, h, retestClass, retestObjective, task.Succeeded, "", true)
	asking := seedFromReal(t, h, retestClass, retestObjective, task.WaitingInput, "", false)
	_, rem := seedRemedy(t, h, retestClass, map[string]any{"metric": "repeat_failure_count", "value": 3.0}, []map[string]any{
		{"task_id": ok1, "kind": "exposing", "from": from}, {"task_id": asking, "kind": "exposing", "from": from}})
	h.eng.EvaluateRemedies(ctx)
	got, _ := task.Get(ctx, h.st.R, asking)
	if got.State != task.Failed || got.ReasonCode == nil || *got.ReasonCode != taxonomy.InputTimeout {
		t.Fatalf("waiting re-test not released: %s %v", got.State, got.ReasonCode)
	}
	h.eng.EvaluateRemedies(ctx)
	status, _, after := remedyRow(t, h, rem)
	if status != "promoted" || after["inconclusive_runs"] != 1.0 || after["exposing_runs"] != 1.0 {
		t.Fatalf("%s %+v", status, after)
	}
}

// A draft that could not run (no provider) waits and is tried again; it is escalated only after the last
// attempt. A draft that ran and was refused is escalated at once.
func TestRemedyDraftTransientFailureWaitsInsteadOfEscalating(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	mk := func(key string) (string, string) {
		var lim, rem string
		h.st.Write(ctx, func(tx *sql.Tx) error {
			l, _, err := h.eng.recordLimitation(ctx, tx, &Limitation{Category: "quality", TaskClass: retestClass, DetectedBy: "runtime_monitor",
				Status: "remedy_proposed", Title: key}, key)
			if err != nil {
				return err
			}
			lim = l.ID
			h.eng.addRemedy(ctx, tx, l.ID, taxonomy.R2, "proposed", "seeded", map[string]any{"task_class": retestClass})
			return tx.QueryRow("select id from limitation_remedies where limitation_id=?", l.ID).Scan(&rem)
		})
		return lim, rem
	}
	lim, rem := mk("code:a")
	down := errors.New("all_routes_failed: provider error 0: route circuit is open")
	for i := 1; i < draftMaxAttempts; i++ {
		h.eng.draftFailed(ctx, rem, lim, down)
		status, art, _ := remedyRow(t, h, rem)
		if status != "proposed" || art["draft_attempts"] != float64(i) || art["next_draft_at"] == nil || art["task_class"] != retestClass {
			t.Fatalf("attempt %d: %s %+v", i, status, art)
		}
	}
	// while it waits, the loop does not draft it again
	h.eng.ledgerCfg.ImprovementUSDDaily = 1
	h.eng.ActOnRemedies(ctx)
	if _, art, _ := remedyRow(t, h, rem); art["draft_attempts"] != float64(draftMaxAttempts-1) {
		t.Fatalf("drafted during the pause: %+v", art)
	}
	h.eng.draftFailed(ctx, rem, lim, down)
	if status, _, _ := remedyRow(t, h, rem); status != "escalated" {
		t.Fatalf("last attempt not escalated: %s", status)
	}
	lim2, rem2 := mk("code:b")
	h.eng.draftFailed(ctx, rem2, lim2, errors.New("protected_topic"))
	if status, _, _ := remedyRow(t, h, rem2); status != "escalated" {
		t.Fatalf("a refused draft must go to the owner: %s", status)
	}
}
