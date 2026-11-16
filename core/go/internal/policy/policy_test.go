package policy

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

func setup(t *testing.T) (*store.Store, *task.Task) {
	t.Helper()
	d := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tk := &task.Task{Objective: "x", ConfigHash: "h"}
	s.Write(context.Background(), func(tx *sql.Tx) error { return task.Insert(context.Background(), tx, tk) })
	return s, tk
}

func mint(t *testing.T, s *store.Store, taskID string, gen int64, ttl int64) (string, *Grant) {
	var tok string
	var g *Grant
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		tok, g, err = Mint(context.Background(), tx, taskID, gen, Scopes{Tools: []string{"web.fetch"}}, ids.NowMs()+ttl, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok, g
}

func TestGrantVerification(t *testing.T) {
	s, tk := setup(t)
	ctx := context.Background()
	tok, g := mint(t, s, tk.ID, 1, 60_000)
	if got, err := Verify(ctx, s.R, tok, tk.ID, 1); err != nil || got.ID != g.ID || !got.Scopes.HasTool("web.fetch") || got.Scopes.HasTool("workspace.write") {
		t.Fatalf("valid token rejected: %v", err)
	}
	var stored string
	s.R.QueryRow("select token_sha256 from capability_grants").Scan(&stored)
	if stored == tok {
		t.Fatal("raw token stored")
	}
	cases := map[string]func() error{
		"wrong task":       func() error { _, err := Verify(ctx, s.R, tok, "OTHER", 1); return err },
		"wrong generation": func() error { _, err := Verify(ctx, s.R, tok, tk.ID, 2); return err },
		"forged token":     func() error { _, err := Verify(ctx, s.R, tok[:len(tok)-2]+"AA", tk.ID, 1); return err },
		"short token":      func() error { _, err := Verify(ctx, s.R, "abc", tk.ID, 1); return err },
	}
	for name, f := range cases {
		if err := f(); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	expTok, _ := mint(t, s, tk.ID, 1, -1)
	if _, err := Verify(ctx, s.R, expTok, tk.ID, 1); !errors.Is(err, ErrDenied) {
		t.Error("expired grant accepted")
	}
	s.Write(ctx, func(tx *sql.Tx) error { return RevokeTask(ctx, tx, tk.ID) })
	if _, err := Verify(ctx, s.R, tok, tk.ID, 1); !errors.Is(err, ErrDenied) {
		t.Error("revoked grant accepted")
	}
}

func TestTiers(t *testing.T) {
	if CheckTier(A1) != ErrTierUnsupported {
		t.Fatal("A1 must be denied in P1")
	}
	if CheckTier(A3) != ErrForbidden || CheckTier(A0) != nil || CheckTier(A2) != nil {
		t.Fatal("tier mapping")
	}
	w := Manifest["workspace.write"]
	if w.Tier(map[string]any{"path": "reports/a.md"}) != A0 || w.Tier(map[string]any{"path": "published/a.md"}) != A2 ||
		w.Tier(map[string]any{"path": ".aura/x"}) != A3 {
		t.Fatal("workspace.write tiers")
	}
}

func approval(t *testing.T, s *store.Store, a Action, ttl int64) *Approval {
	var ap *Approval
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		ap, err = RequestApproval(context.Background(), tx, a, ttl)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ap
}

func baseActionFor(t *testing.T, s *store.Store, taskID string) Action {
	t.Helper()
	tool := "workspace.write"
	steps := []task.Step{{StepNo: 1, Description: "write", ToolID: &tool, SideEffect: "write_idempotent"}}
	if err := s.Write(context.Background(), func(tx *sql.Tx) error { return task.InsertSteps(context.Background(), tx, taskID, steps) }); err != nil {
		t.Fatal(err)
	}
	_, g := mint(t, s, taskID, 1, 60_000)
	return Action{ToolID: tool, ToolVersion: "1", Arguments: map[string]any{"path": "published/r.md", "content": "hello"},
		Target: "published/r.md", SideEffect: "write_idempotent", TaskID: taskID, StepID: steps[0].ID, GrantID: g.ID}
}

// SEC-08: changing any parameter invalidates; expired and reused approvals are rejected.
func TestApprovalIntegrity(t *testing.T) {
	s, tk := setup(t)
	ctx := context.Background()
	a := baseActionFor(t, s, tk.ID)
	ap := approval(t, s, a, 60_000)

	err := s.Write(ctx, func(tx *sql.Tx) error { _, err := Decide(ctx, tx, ap.ID, true, "0000", "owner"); return err })
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("wrong hash approved: %v", err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error { _, err := Decide(ctx, tx, ap.ID, true, ap.ActionHash, "owner"); return err }); err != nil {
		t.Fatal(err)
	}
	changed := a
	changed.Arguments = map[string]any{"path": "published/r.md", "content": "hello!"}
	if changed.Hash() == a.Hash() {
		t.Fatal("hash ignores parameters")
	}
	err = s.Write(ctx, func(tx *sql.Tx) error { return Consume(ctx, tx, ap.ID, changed) })
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("changed action consumed: %v", err)
	}
	got, _ := GetApproval(ctx, s.R, ap.ID)
	if got.Status != "invalidated" {
		t.Fatalf("status %s", got.Status)
	}

	ap2 := approval(t, s, a, 60_000)
	s.Write(ctx, func(tx *sql.Tx) error { _, err := Decide(ctx, tx, ap2.ID, true, ap2.ActionHash, "owner"); return err })
	if err := s.Write(ctx, func(tx *sql.Tx) error { return Consume(ctx, tx, ap2.ID, a) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return Consume(ctx, tx, ap2.ID, a) }); !errors.Is(err, ErrNotPending) {
		t.Fatalf("reused approval: %v", err)
	}

	ap3 := approval(t, s, a, -1)
	err = s.Write(ctx, func(tx *sql.Tx) error { _, err := Decide(ctx, tx, ap3.ID, true, ap3.ActionHash, "owner"); return err })
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired approval approved: %v", err)
	}
}

func TestNewRequestInvalidatesPrevious(t *testing.T) {
	s, tk := setup(t)
	a := baseActionFor(t, s, tk.ID)
	first := approval(t, s, a, 60_000)
	approval(t, s, a, 60_000)
	got, _ := GetApproval(context.Background(), s.R, first.ID)
	if got.Status != "invalidated" {
		t.Fatal(got.Status)
	}
	p, _ := Pending(context.Background(), s.R)
	if len(p) != 1 {
		t.Fatalf("pending %d", len(p))
	}
}

func TestAuthStrengthOf(t *testing.T) {
	for by, want := range map[string]string{
		"owner(web, step-up)":   "owner_web_step_up_totp",
		"owner(web)":            "owner_web_session",
		"owner":                 "owner_token",
		"member:Sari (finance)": "member_token",
		"member:Budi (admin)":   "member_token",
		"":                      "unknown",
		"worker":                "unknown",
	} {
		if got := AuthStrengthOf(by); got != want {
			t.Errorf("AuthStrengthOf(%q)=%s want %s", by, got, want)
		}
	}
}

func TestApprovalDecisionRecordsWhoHowAndWhen(t *testing.T) {
	s, tk := setup(t)
	ctx := context.Background()
	a := baseActionFor(t, s, tk.ID)
	ap := approval(t, s, a, 60_000)
	if len(ap.Nonce) != 32 || ap.RiskClass != "R2" || ap.PolicyVersion != RiskPolicyVersion {
		t.Fatalf("%+v", ap)
	}
	tk2 := &task.Task{Objective: "y", ConfigHash: "h"}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return task.Insert(ctx, tx, tk2) }); err != nil {
		t.Fatal(err)
	}
	other := approval(t, s, baseActionFor(t, s, tk2.ID), 60_000)
	if other.Nonce == ap.Nonce {
		t.Fatal("two approvals share a nonce")
	}
	var got *Approval
	s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = Decide(ctx, tx, other.ID, true, other.ActionHash, "owner(web, step-up)")
		return err
	})
	if got.AuthStrength != "owner_web_step_up_totp" {
		t.Fatalf("%+v", got)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return Consume(ctx, tx, other.ID, other.Action()) }); err != nil {
		t.Fatal(err)
	}
	if c, _ := GetApproval(ctx, s.R, other.ID); c.ConsumedAt == nil || c.Status != "consumed" {
		t.Fatalf("%+v", c)
	}
	// Revoke: pending and approved can be withdrawn; consumed cannot.
	if err := s.Write(ctx, func(tx *sql.Tx) error { _, err := RevokeApproval(ctx, tx, other.ID, "owner", "late"); return err }); !errors.Is(err, ErrNotPending) {
		t.Fatalf("consumed approval revoked: %v", err)
	}
	var r *Approval
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		r, err = RevokeApproval(ctx, tx, ap.ID, "owner", "wrong amount")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r.Status != "invalidated" || r.RevokedAt == nil || r.RevokeReason != "wrong amount" {
		t.Fatalf("%+v", r)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error { _, err := Decide(ctx, tx, ap.ID, true, ap.ActionHash, "owner"); return err }); !errors.Is(err, ErrNotPending) {
		t.Fatalf("revoked approval approved: %v", err)
	}
}
