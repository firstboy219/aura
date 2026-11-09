package engine

import (
	"context"
	"database/sql"
	"encoding/json"
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
func seedFromReal(t *testing.T, h *harness, class, objective string, state task.State, code string, verified bool) {
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
	it, err := h.eng.improvementTask(ctx, "cap test")
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
