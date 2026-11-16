package odi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aura.local/core/internal/store"
)

func TestClassifyByPath(t *testing.T) {
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{[]string{"docs/odi/README.md"}, L0},
		{[]string{"README.md"}, L0},
		{[]string{"skills/logika/ketidakpastian.md"}, L1},
		{[]string{"benchmarks/reports/x.md"}, L1},
		{[]string{"core/go/internal/engine/loop.go"}, L2},
		{[]string{"contracts/api/openapi.yaml"}, L2},
		{[]string{"intelligence/python/aura_worker/main.py"}, L2},
		{[]string{"core/go/internal/policy/policy.go"}, L3},
		{[]string{"core/go/internal/vault/vault.go"}, L3},
		{[]string{"core/go/internal/nexus/fallback.go"}, L3},
		{[]string{"core/go/internal/device/device.go"}, L3},
		{[]string{"core/go/internal/store/migrations/0024_x.sql"}, L3},
		{[]string{"deploy/docker-compose.yml"}, L3},
		{[]string{"contracts/odi/acceptance-catalogue.json"}, L3},
		{[]string{"AGENTS.md"}, L3},
		{[]string{"docs/a.md", "skills/x.md", "core/go/internal/engine/x.go"}, L2},
		{[]string{"docs/a.md", "deploy/aura.service"}, L3},
		{[]string{"something/unknown.bin"}, L2},
		{[]string{"./core/go/internal/policy/../policy/policy.go"}, L3},
	} {
		got, reasons, err := Classify(c.paths)
		if err != nil || got != c.want || len(reasons) == 0 {
			t.Errorf("%v -> %s %v %v, want %s", c.paths, got, reasons, err, c.want)
		}
	}
	for _, bad := range [][]string{nil, {}, {"../etc/passwd"}, {"/etc/shadow"}, {"a\x00b"}, {""}} {
		if _, _, err := Classify(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The protected list cannot rot: every entry must still exist in the repository.
func TestEveryProtectedPathExists(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	for _, im := range immutable {
		if _, err := os.Stat(filepath.Join(root, im.prefix)); err != nil {
			t.Errorf("protected path %q no longer exists: %v", im.prefix, err)
		}
	}
}

func TestWeakensTests(t *testing.T) {
	benign := "diff --git a/core/go/internal/x/x.go b/core/go/internal/x/x.go\n--- a/core/go/internal/x/x.go\n+++ b/core/go/internal/x/x.go\n@@\n-old\n+new\n" +
		"diff --git a/core/go/internal/x/x_test.go b/core/go/internal/x/x_test.go\n@@\n-\tif got != 1 {\n+\tif got != 2 {\n+\t\tt.Fatalf(\"x\")\n"
	if f := WeakensTests(benign); len(f) != 0 {
		t.Fatalf("benign diff flagged: %v", f)
	}
	for name, diff := range map[string]string{
		"deleted test file":  "diff --git a/a/x_test.go b/a/x_test.go\ndeleted file mode 100644\n--- a/a/x_test.go\n+++ /dev/null\n-func TestX(t *testing.T) {\n-\tt.Fatal(1)\n-}\n",
		"skipped test":       "diff --git a/a/x_test.go b/a/x_test.go\n@@\n func TestX(t *testing.T) {\n+\tt.Skip(\"flaky\")\n",
		"python skip":        "diff --git a/tests/test_x.py b/tests/test_x.py\n@@\n+@pytest.mark.skip\n def test_x():\n",
		"assertions removed": "diff --git a/a/x_test.go b/a/x_test.go\n@@\n-\t\tt.Fatalf(\"a\")\n-\t\tt.Errorf(\"b\")\n+\t\t// relaxed\n",
		"status raised":      "diff --git a/contracts/odi/acceptance-catalogue.json b/contracts/odi/acceptance-catalogue.json\n@@\n+  \"status\": \"met\",\n",
	} {
		if f := WeakensTests(diff); len(f) == 0 {
			t.Errorf("%s: not detected", name)
		}
	}
	// a skip in production code is not a test weakening
	if f := WeakensTests("diff --git a/a/x.go b/a/x.go\n@@\n+\tt.Skip()\n"); len(f) != 0 {
		t.Errorf("production file flagged: %v", f)
	}
}

func goodProposal(components ...string) Proposal {
	return Proposal{Title: "Fix X", Symptom: "x fails", Evidence: []string{"log line"}, RootCauseConfidence: "medium", Components: components,
		Risks: []string{"may change y"}, TestPlan: "run the suite", RollbackPlan: "git revert", ExpectedOutcome: "x works"}
}

func TestProposalNeedsWhatTheLevelRequires(t *testing.T) {
	if bad := (Proposal{}).Validate(L0); len(bad) < 4 {
		t.Fatalf("an empty proposal passed: %v", bad)
	}
	p := goodProposal("docs/a.md")
	p.TestPlan, p.RollbackPlan, p.Risks = "", "", nil
	if len(p.Validate(L0)) != 0 || len(p.Validate(L1)) != 0 {
		t.Fatal("L0/L1 do not need tests, rollback and risks")
	}
	if bad := p.Validate(L2); len(bad) != 3 {
		t.Fatalf("L2 needs test plan, rollback plan and risks: %v", bad)
	}
	p.RootCauseConfidence = "certain"
	if len(p.Validate(L0)) == 0 {
		t.Fatal("an invented confidence word was accepted")
	}
}

func newChanges(t *testing.T) *Changes {
	t.Helper()
	d := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Changes{St: st}
}

var (
	owner  = Actor{Name: "owner", Owner: true}
	ownerT = Actor{Name: "owner", Owner: true, StepUp: true}
	agent  = Actor{Name: "agent:aura"}
	member = Actor{Name: "member:sari"}
)

func TestChangeLifecycleAndApprovalRules(t *testing.T) {
	c := newChanges(t)
	ctx := context.Background()

	// L0 is approved automatically after the checks
	l0, err := c.Create(ctx, agent, goodProposal("docs/odi/x.md"), "")
	if err != nil || l0.Level != L0 || l0.Status != "approved" || !strings.HasPrefix(l0.ApprovedBy, "auto:") {
		t.Fatalf("%v %+v", err, l0)
	}
	// L1 waits for the owner unless pre-approved
	l1, _ := c.Create(ctx, agent, goodProposal("skills/x.md"), "")
	if l1.Status != "awaiting_approval" {
		t.Fatalf("L1 status %s", l1.Status)
	}
	c.L1Preapproved = func(context.Context) bool { return true }
	l1b, _ := c.Create(ctx, agent, goodProposal("skills/y.md"), "")
	c.L1Preapproved = nil
	if l1b.Status != "approved" {
		t.Fatalf("a pre-approved L1 was not approved: %s", l1b.Status)
	}

	// L2: an agent proposes, a member cannot approve, the proposer cannot approve its own, the owner token alone cannot approve an agent's L2, the owner with a second factor can
	l2, err := c.Create(ctx, agent, goodProposal("core/go/internal/engine/loop.go"), "diff --git a/core/go/internal/engine/loop.go b/core/go/internal/engine/loop.go\n+x\n")
	if err != nil || l2.Level != L2 || l2.Status != "awaiting_approval" {
		t.Fatalf("%v %+v", err, l2)
	}
	for name, who := range map[string]Actor{"member": member, "the agent itself": agent, "the owner token without a second factor": owner} {
		if _, err := c.Decide(ctx, l2.ID, who, true, ""); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s approved an agent's L2: %v", name, err)
		}
	}
	got, err := c.Decide(ctx, l2.ID, ownerT, true, "looks right")
	if err != nil || got.Status != "approved" || got.ApprovedBy != "owner" {
		t.Fatalf("%v %+v", err, got)
	}
	if _, err := c.Decide(ctx, l2.ID, ownerT, true, ""); !errors.Is(err, ErrChange) {
		t.Fatalf("an approved change was decided again: %v", err)
	}

	// L3 always needs the owner with a second factor, whoever proposed it
	l3, err := c.Create(ctx, owner, goodProposal("core/go/internal/policy/policy.go"), "")
	if err != nil || l3.Level != L3 {
		t.Fatalf("%v %+v", err, l3)
	}
	if _, err := c.Decide(ctx, l3.ID, owner, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an L3 change was approved without a second factor: %v", err)
	}
	if _, err := c.Decide(ctx, l3.ID, ownerT, true, ""); err != nil {
		t.Fatalf("%v", err)
	}

	// a change that weakens its tests is blocked and cannot be approved
	weak, _ := c.Create(ctx, member, goodProposal("core/go/internal/engine/x.go"), "diff --git a/core/go/internal/engine/x_test.go b/core/go/internal/engine/x_test.go\ndeleted file mode 100644\n")
	if weak.Status != "blocked" || len(weak.Weakening) == 0 {
		t.Fatalf("%+v", weak)
	}
	if _, err := c.Decide(ctx, weak.ID, ownerT, true, ""); !errors.Is(err, ErrChange) {
		t.Fatalf("a blocked change was approved: %v", err)
	}

	// rollout: legal moves only; acceptance needs evidence and the owner
	if _, err := c.Move(ctx, l2.ID, ownerT, "accepted", "", nil); !errors.Is(err, ErrChange) {
		t.Fatalf("accepted straight from approved: %v", err)
	}
	if _, err := c.Move(ctx, l2.ID, member, "in_canary", "", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member started the rollout of an L2 change: %v", err)
	}
	if _, err := c.Move(ctx, l2.ID, ownerT, "in_canary", "5% traffic", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Move(ctx, l2.ID, ownerT, "deployed", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Move(ctx, l2.ID, ownerT, "accepted", "", []Evid{{Kind: "test_run", OK: true}}); !errors.Is(err, ErrChange) {
		t.Fatalf("accepted without post-deployment health evidence: %v", err)
	}
	if _, err := c.Move(ctx, l2.ID, ownerT, "accepted", "", []Evid{{Kind: "post_deploy_health", OK: false}}); !errors.Is(err, ErrChange) {
		t.Fatalf("accepted on failing evidence: %v", err)
	}
	if _, err := c.Move(ctx, l2.ID, member, "accepted", "", []Evid{{Kind: "post_deploy_health", OK: true}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member accepted a change: %v", err)
	}
	done, err := c.Move(ctx, l2.ID, ownerT, "accepted", "ok", []Evid{{Kind: "post_deploy_health", OK: true, Detail: "health 200, error rate 0"}})
	if err != nil || done.Status != "accepted" || len(done.Evidence) != 1 || done.Evidence[0].At == 0 {
		t.Fatalf("%v %+v", err, done)
	}
	// rollback needs a reason
	l1c, _ := c.Create(ctx, owner, goodProposal("skills/z.md"), "")
	c.Decide(ctx, l1c.ID, owner, true, "")
	c.Move(ctx, l1c.ID, owner, "deployed", "", nil)
	if _, err := c.Move(ctx, l1c.ID, owner, "rolled_back", "", nil); !errors.Is(err, ErrChange) {
		t.Fatalf("a rollback without a reason: %v", err)
	}
	if rb, err := c.Move(ctx, l1c.ID, owner, "rolled_back", "regression in benchmark", nil); err != nil || rb.Status != "rolled_back" {
		t.Fatalf("%v %+v", err, rb)
	}

	// the whole story is audited and the chain still holds
	var n int
	c.St.R.QueryRow("select count(*) from audit_events where type like 'CHANGE_%'").Scan(&n)
	if n < 10 {
		t.Fatalf("only %d change audit rows", n)
	}
	list, _ := c.List(ctx, "accepted")
	if len(list) != 1 {
		t.Fatalf("list accepted: %d", len(list))
	}
}

func TestCreateRefusesIncompleteOrOversizedProposals(t *testing.T) {
	c := newChanges(t)
	ctx := context.Background()
	if _, err := c.Create(ctx, owner, Proposal{Title: "x", Components: []string{"core/go/internal/engine/x.go"}}, ""); !errors.Is(err, ErrChange) {
		t.Fatalf("%v", err)
	}
	if _, err := c.Create(ctx, owner, goodProposal("core/go/internal/engine/x.go"), strings.Repeat("x", maxDiff+1)); !errors.Is(err, ErrChange) {
		t.Fatalf("%v", err)
	}
	if _, err := c.Create(ctx, owner, goodProposal("../outside"), ""); !errors.Is(err, ErrChange) {
		t.Fatalf("%v", err)
	}
}
