package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"aura.local/core/internal/task"
)

// ODI A5 (ADR-0074): the owner can cap a tool per day; the Core refuses the call that would exceed it and audits the denial. No quota row = unchanged behaviour.
func TestToolQuotaDeniesTheCallPastTheLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if _, err := h.st.R.Exec("select 1"); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "insert into settings(key, value, updated_at) values(?,?,?)", ToolQuotaSetting, `{"web.fetch":1}`, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tk := h.submit(rs1Objective(h.host), nil)
	h.wait(tk.ID, task.Succeeded, task.Failed)
	got := strings.Join(deniedReasons(t, h, tk.ID), ",")
	if !strings.Contains(got, "tool_quota_exceeded") {
		t.Fatalf("no quota denial: %q", got)
	}
}

func TestNoQuotaMeansNoLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	tk := h.submit(rs1Objective(h.host), nil)
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded || strings.Contains(strings.Join(deniedReasons(t, h, tk.ID), ","), "tool_quota") {
		t.Fatalf("%s %s", done.State, deref(done.Reason))
	}
}

func TestLoadToolQuotasDefaultsToNone(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	q, err := LoadToolQuotas(context.Background(), h.st.R)
	if err != nil || len(q) != 0 {
		t.Fatalf("%v %v", q, err)
	}
}
