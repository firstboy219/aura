package engine

import (
	"context"
	"database/sql"
	"testing"

	"aura.local/core/internal/task"
)

// interruptedTask submits a task with the given recovery policy, then fakes a
// Core crash in mid-run: the task is PLANNING under a lease of a dead run.
func interruptedTask(t *testing.T, recovery string) (*harness, string) {
	t.Helper()
	h := newHarness(t, harnessOpts{noStart: true})
	tk, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: rs1Objective(h.host), AllowDomains: []string{h.host}, Recovery: recovery})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("update tasks set state='PLANNING', lease_owner='dead-run' where id=?", tk.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.start() // Run recovers interrupted tasks first
	return h, tk.ID
}

// Blueprint §8.2 recovery policy: auto resumes, manual pauses for the owner,
// never fails with a canonical code.
func TestRecoveryPolicy(t *testing.T) {
	t.Run("auto", func(t *testing.T) {
		h, id := interruptedTask(t, "")
		if done := h.wait(id, task.Succeeded, task.Failed, task.Paused); done.State != task.Succeeded {
			t.Fatalf("auto: %s %s", done.State, deref(done.Reason))
		}
	})
	t.Run("manual", func(t *testing.T) {
		h, id := interruptedTask(t, "manual")
		p := h.wait(id, task.Paused, task.Succeeded, task.Failed)
		if p.State != task.Paused || deref(p.Reason) != "interrupted_recovery_manual" {
			t.Fatalf("manual: %s %s", p.State, deref(p.Reason))
		}
		if _, err := h.eng.Resume(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		if done := h.wait(id, task.Succeeded, task.Failed); done.State != task.Succeeded {
			t.Fatalf("after resume: %s", done.State)
		}
	})
	t.Run("never", func(t *testing.T) {
		h, id := interruptedTask(t, "never")
		f := h.wait(id, task.Failed, task.Succeeded, task.Paused)
		if f.State != task.Failed || deref(f.ReasonCode) != "INTERRUPTED" {
			t.Fatalf("never: %s %s", f.State, deref(f.ReasonCode))
		}
	})
	t.Run("invalid", func(t *testing.T) {
		h := newHarness(t, harnessOpts{})
		if _, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: "x", Recovery: "sometimes"}); err == nil {
			t.Fatal("invalid recovery policy accepted")
		}
	})
}
