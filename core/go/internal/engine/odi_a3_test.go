package engine

import (
	"context"
	"strings"
	"testing"

	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// ODI A3 (ADR-0072): approvals carry a nonce, a risk class under a named policy version and how the approver authenticated; a consumed
// approval records when; the audit log stays a verifiable hash chain through a whole approved task.
func TestApprovalRecordsNonceRiskAndAuthStrength(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	obj := strings.Replace(rs1Objective(h.host), "reports/summary.md", "published/report.md", 1)
	tk := h.submit(obj, nil)
	h.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(context.Background(), h.st.R)
	if len(aps) != 1 {
		t.Fatalf("%+v", aps)
	}
	ap := aps[0]
	if len(ap.Nonce) != 32 || ap.RiskClass == "" || ap.PolicyVersion != policy.RiskPolicyVersion || ap.AuthStrength != "" {
		t.Fatalf("pending approval: nonce=%q risk=%q policy=%q auth=%q", ap.Nonce, ap.RiskClass, ap.PolicyVersion, ap.AuthStrength)
	}
	if _, err := h.eng.Approve(context.Background(), ap.ID, ap.ActionHash, "owner(web, step-up)"); err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	got, _ := policy.GetApproval(context.Background(), h.st.R, ap.ID)
	if got.Status != "consumed" || got.ConsumedAt == nil || got.AuthStrength != "owner_web_step_up_totp" || got.Nonce != ap.Nonce {
		t.Fatalf("%+v", got)
	}
	st, err := policy.VerifyAuditChain(context.Background(), h.st.R)
	if err != nil || !st.Valid || st.Chained == 0 {
		t.Fatalf("audit chain after an approved task: %+v %v", st, err)
	}
}

func TestRevokeApprovalFailsTheTaskAndRevokesItsGrants(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	obj := strings.Replace(rs1Objective(h.host), "reports/summary.md", "published/report.md", 1)
	tk := h.submit(obj, nil)
	h.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(context.Background(), h.st.R)
	ap := aps[0]
	if _, err := h.eng.RevokeApproval(context.Background(), ap.ID, "owner", "changed my mind"); err != nil {
		t.Fatal(err)
	}
	done := h.wait(tk.ID, task.Failed)
	if done.State != task.Failed {
		t.Fatalf("task %s", done.State)
	}
	got, _ := policy.GetApproval(context.Background(), h.st.R, ap.ID)
	if got.Status != "invalidated" || got.RevokedAt == nil || got.RevokeReason != "changed my mind" {
		t.Fatalf("%+v", got)
	}
	// A revoked approval can neither be approved nor revoked again, and the action never ran.
	if _, err := h.eng.Approve(context.Background(), ap.ID, ap.ActionHash, "owner"); err == nil {
		t.Fatal("a revoked approval was approved")
	}
	if _, err := h.eng.RevokeApproval(context.Background(), ap.ID, "owner", ""); err == nil {
		t.Fatal("a revoked approval was revoked twice")
	}
	var live int
	h.st.R.QueryRow("select count(*) from capability_grants where task_id=? and revoked_at is null", tk.ID).Scan(&live)
	if live != 0 {
		t.Fatalf("%d grants still live after the approval was revoked", live)
	}
	var n int
	h.st.R.QueryRow("select count(*) from audit_events where type='APPROVAL_REVOKED' and task_id=?", tk.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("APPROVAL_REVOKED audit rows: %d", n)
	}
	if reason := deref(done.Reason); !strings.HasPrefix(reason, "approval_revoked") {
		t.Fatalf("reason %q", reason)
	}
}
