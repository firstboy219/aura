package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

const labGuidance = "Fetch only pages that the objective names; when a named page fails, use the closest page the objective allows."

// labModel: without the variant's guidance the planner fetches a missing page
// (the limitation); with it (when helps) it fetches the right one.
func labModel(helps bool) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "brain-remedy module"):
			b, _ := json.Marshal(map[string]any{"module": "planner", "guidance": labGuidance, "rationale": "plans fetch missing pages"})
			return gatewaytest.Reply{Content: string(b)}
		case isSys(req, "planning module"):
			url := "http://fixture.test/missing"
			if helps && strings.Contains(req.Messages[0].Content, labGuidance) {
				url = "http://fixture.test/one"
			}
			return planWith([]string{"the summary file summarizes the page with its source link"},
				gatewaytest.Step(1, "fetch", "web.fetch", map[string]any{"url": url}),
				gatewaytest.Step(2, "write summary", "workspace.write", map[string]any{"path": "reports/summary.md"}))
		case isSys(req, "replanning module"):
			return planWith([]string{}, gatewaytest.Step(2, "write summary", "workspace.write", map[string]any{"path": "reports/summary.md"}))
		case isSys(req, "reflection module"):
			return gatewaytest.Reply{Content: `{"lesson":null,"fact":null,"skill":null,"preference":null}`}
		}
		return goodModel(r, req)
	}
}

// labSetup opens a planning-strategy limitation whose skill remedy failed, so
// the next rung is an R4 brain remedy.
func labSetup(t *testing.T, h *harness) *Limitation {
	t.Helper()
	ctx := context.Background()
	const class = "web.fetch+workspace.write"
	for i := 0; i < 8; i++ {
		seedFromReal(t, h, class, "Fetch http://fixture.test/missing and summarize into reports/summary.md", task.Failed, taxonomy.PlanInvalid, false)
	}
	for i := 0; i < 2; i++ {
		seedFromReal(t, h, class, "Fetch http://fixture.test/one and summarize into reports/summary.md", task.Succeeded, "", true)
	}
	h.eng.ledgerCfg.ImprovementUSDDaily = 1
	if _, err := h.eng.Detect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Diagnose(ctx); err != nil {
		t.Fatal(err)
	}
	var lim *Limitation
	for _, l := range limByKey(t, h, "") {
		if l.Diagnosis["cause"] == taxonomy.CausePlanStrategy {
			lim = l
		}
	}
	if lim == nil {
		t.Fatalf("no planning-strategy limitation: %s", dumpLedger(t, h))
	}
	// The skill remedy (R2) did not help.
	h.st.Write(ctx, func(tx *sql.Tx) error {
		tx.Exec("update limitation_remedies set status='rolled_back' where limitation_id=?", lim.ID)
		_, err := tx.Exec("update limitations set status='rolled_back' where id=?", lim.ID)
		return err
	})
	if h.eng.proposeBrainRemedy(ctx, lim.ID) {
		t.Fatal("an R4 remedy was proposed while AURA Lab is disabled")
	}
	h.eng.cfg.Lab.Enabled = true
	if !h.eng.proposeBrainRemedy(ctx, lim.ID) {
		t.Fatal("R4 remedy not proposed")
	}
	if h.eng.proposeBrainRemedy(ctx, lim.ID) {
		t.Fatal("R4 proposed twice for one limitation")
	}
	return lim
}

func waitVariant(t *testing.T, h *harness, not string) *BrainVariant {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		h.eng.EvaluateLab(ctx)
		vs, _ := h.eng.ListBrainVariants(ctx, "")
		if len(vs) == 1 && vs[0].Status != not {
			return vs[0]
		}
		time.Sleep(200 * time.Millisecond)
	}
	vs, _ := h.eng.ListBrainVariants(ctx, "")
	t.Fatalf("variant still %s: %+v", not, vs)
	return nil
}

// D.1 P6: an R4 brain remedy flows from the ledger through AURA Lab (paired
// baseline/variant runs, cleanup), owner approval bound to the sha256, canary
// rollout and promotion; the owner can roll it back.
func TestP6BrainRemedyThroughLab(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: labModel(true)})
	ctx := context.Background()
	lim := labSetup(t, h)
	h.eng.ActOnRemedies(ctx)
	vs, _ := h.eng.ListBrainVariants(ctx, "lab_testing")
	if len(vs) != 1 || vs[0].Module != "planner" || vs[0].Guidance != labGuidance {
		t.Fatalf("variant not drafted: %+v %s", vs, dumpLedger(t, h))
	}
	var runs, baseline int
	h.st.R.QueryRow("select count(*), coalesce(sum(arm='baseline'),0) from lab_runs where variant_id=?", vs[0].ID).Scan(&runs, &baseline)
	if runs != 10 || baseline != 5 { // 3 exposing + 2 regression tasks, each run with and without the variant
		t.Fatalf("lab runs %d (baseline %d)", runs, baseline)
	}
	v := waitVariant(t, h, "lab_testing")
	if v.Status != "awaiting_approval" || v.Lab["passed"] != true {
		t.Fatalf("lab verdict: %s %+v", v.Status, v.Lab)
	}
	// Lab isolation and cleanup: no approvals asked, workspaces removed, improvement budget carried the cost.
	var approvals, uncleaned int
	h.st.R.QueryRow("select count(*) from approvals").Scan(&approvals)
	h.st.R.QueryRow("select count(*) from lab_runs where cleaned_at is null").Scan(&uncleaned)
	if approvals != 0 || uncleaned != 0 {
		t.Fatalf("approvals %d, uncleaned lab runs %d", approvals, uncleaned)
	}
	rows, _ := h.st.R.Query("select task_id from lab_runs")
	for rows.Next() {
		var id string
		rows.Scan(&id)
		if _, err := os.Stat(filepath.Join(h.eng.ws.Root(), id)); !os.IsNotExist(err) {
			t.Fatalf("lab workspace %s not cleaned up (%v)", id, err)
		}
	}
	rows.Close()
	// Nothing is in service before the owner approves.
	if g, _ := h.eng.brainGuidance(ctx, &task.Task{ID: "x", RootTaskID: "x", CreatedAt: ids.NowMs()}); g != nil {
		t.Fatalf("guidance in service before approval: %v", g)
	}
	if _, err := h.eng.DecideBrain(ctx, v.ID, true, strings.Repeat("0", 64), "owner"); !errors.Is(err, policy.ErrMismatch) {
		t.Fatalf("approval with the wrong hash: %v", err)
	}
	h.eng.cfg.Lab.CanaryPercent, h.eng.cfg.Lab.CanaryMinTasks = 100, 2
	if v, err := h.eng.DecideBrain(ctx, v.ID, true, v.SHA256, "owner"); err != nil || v.Status != "canary" {
		t.Fatalf("approve: %v %+v", err, v)
	}
	for i := 0; i < 2; i++ {
		tk := h.submitTools("Fetch http://fixture.test/missing and summarize into reports/summary.md", "web.fetch", "workspace.write")
		if done := h.wait(tk.ID, task.Succeeded, task.Failed); done.State != task.Succeeded {
			t.Fatalf("canary task %s: %s", done.ID, errorDetail(h.events(done.ID)))
		}
	}
	h.eng.EvaluateCanaries(ctx)
	v, _ = h.eng.GetBrainVariant(ctx, v.ID)
	if v.Status != "active" || v.Rollout["canary"] == nil {
		t.Fatalf("canary not promoted: %+v", v)
	}
	l, _ := h.eng.limitation(ctx, lim.ID)
	if l.Status != "resolved" {
		t.Fatalf("limitation %s", l.Status)
	}
	var promoted int
	h.st.R.QueryRow("select count(*) from audit_events where type in ('LAB_PASSED','BRAIN_VARIANT_APPROVED','BRAIN_VARIANT_PROMOTED')").Scan(&promoted)
	if promoted != 3 {
		t.Fatalf("audit trail: %d", promoted)
	}
	if _, err := h.eng.RollbackBrain(ctx, v.ID, "owner", "testing rollback"); err != nil {
		t.Fatal(err)
	}
	if g, _ := h.eng.brainGuidance(ctx, &task.Task{ID: "y", RootTaskID: "y", CreatedAt: ids.NowMs()}); g != nil {
		t.Fatalf("guidance still in service after rollback: %v", g)
	}
}

// A variant that does not beat the current version never reaches the owner.
func TestP6LabRejectsVariantThatDoesNotHelp(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: labModel(false)})
	ctx := context.Background()
	lim := labSetup(t, h)
	h.eng.ActOnRemedies(ctx)
	v := waitVariant(t, h, "lab_testing")
	if v.Status != "lab_failed" || v.Lab["passed"] != false {
		t.Fatalf("verdict %s %+v", v.Status, v.Lab)
	}
	if _, err := h.eng.DecideBrain(ctx, v.ID, true, v.SHA256, "owner"); !errors.Is(err, task.ErrInvalidTransition) {
		t.Fatalf("a failed variant was approvable: %v", err)
	}
	l, _ := h.eng.limitation(ctx, lim.ID)
	if l.Status != "rolled_back" {
		t.Fatalf("limitation %s", l.Status)
	}
}

// A canary that does worse than the baseline is rolled back automatically.
func TestP6CanaryAutoRollback(t *testing.T) {
	h := newHarness(t, harnessOpts{respond: labModel(false)})
	ctx := context.Background()
	h.eng.cfg.Lab.Enabled, h.eng.cfg.Lab.CanaryPercent, h.eng.cfg.Lab.CanaryMinTasks = true, 100, 2
	now := ids.NowMs()
	v := &BrainVariant{ID: ids.New(), Module: "planner", Guidance: labGuidance, SHA256: guidanceHash("planner", labGuidance)}
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`insert into brain_variants(id, module, guidance, sha256, status, lab_json, canary_started_at, created_at, updated_at)
			values(?,?,?,?,'canary',?,?,?,?)`, v.ID, v.Module, v.Guidance, v.SHA256, js(map[string]any{"baseline": map[string]any{"Runs": 2, "OK": 2}}), now, now, now)
		return err
	})
	for i := 0; i < 2; i++ {
		tk := h.submitTools("Fetch http://fixture.test/missing and summarize into reports/summary.md", "web.fetch", "workspace.write")
		h.wait(tk.ID, task.Succeeded, task.Failed)
		evs := h.events(tk.ID)
		applied := false
		for _, e := range evs {
			applied = applied || (e.Type == "BRAIN_GUIDANCE" && strings.Contains(string(e.Data), v.ID))
		}
		if !applied {
			t.Fatal("the canary variant was not applied to a canary task")
		}
	}
	h.eng.EvaluateCanaries(ctx)
	got, _ := h.eng.GetBrainVariant(ctx, v.ID)
	if got.Status != "rolled_back" || got.Rollout["rolled_back_by"] != "core" {
		t.Fatalf("canary not rolled back: %+v", got)
	}
}

// The Core refuses variant drafts that are not plain module guidance.
func TestP6GuidanceGate(t *testing.T) {
	for _, c := range []struct{ module, g string }{{"policy", labGuidance}, {"planner", "short"},
		{"planner", "Use this:\n```python\nimport os\n```"}, {"planner", strings.Repeat("a", 1501)}} {
		if checkGuidance(c.module, c.g) == "" {
			t.Fatalf("accepted %q/%q", c.module, c.g)
		}
	}
	if p := checkGuidance("verifier", labGuidance); p != "" {
		t.Fatal(p)
	}
}

// Improvement work that reads the owner's private tasks is itself private, so
// its model calls never use routes that may log prompts.
func TestImprovementTaskInheritsPrivacy(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	pub := exemplar{Scope: task.Scope{DataClass: "public"}}
	priv := exemplar{Scope: task.Scope{DataClass: "private"}}
	if anyPrivate([]exemplar{pub}) || !anyPrivate([]exemplar{pub}, []exemplar{priv}) {
		t.Fatal("anyPrivate")
	}
	for _, private := range []bool{false, true} {
		it, err := h.eng.improvementTask(ctx, "privacy", private)
		if err != nil {
			t.Fatal(err)
		}
		if it.Scope.IsPublic() == private {
			t.Fatalf("private=%v gave data class %q", private, it.Scope.DataClass)
		}
	}
}
