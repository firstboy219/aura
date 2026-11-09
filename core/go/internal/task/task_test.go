package task

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"aura.local/core/internal/obs"
	"aura.local/core/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	d := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func create(t *testing.T, s *store.Store) *Task {
	tk := &Task{Objective: "do it", Budget: Budget{MaxCostUSD: 0.5, MaxIterations: 5, MaxWallSeconds: 60}, ConfigHash: "h"}
	if err := s.Write(context.Background(), func(tx *sql.Tx) error { return Insert(context.Background(), tx, tk) }); err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestLifecycleAndEvents(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tk := create(t, s)
	for _, to := range []State{Planning, Running, WaitingApproval, Running, Succeeded} {
		if err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := Transition(ctx, tx, tk.ID, to, "", "", "core")
			return err
		}); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
	got, _ := Get(ctx, s.R, tk.ID)
	if got.State != Succeeded || got.StartedAt == nil || got.RootTaskID != tk.ID || got.AgentRole != "main" || got.Depth != 0 {
		t.Fatalf("%+v", got)
	}
	evs, _ := Events(ctx, s.R, tk.ID, 0, true)
	if len(evs) != 6 || evs[0].Type != "TASK_CREATED" || evs[5].Seq != 6 {
		t.Fatalf("events %+v", evs)
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tk := create(t, s)
	for _, to := range []State{Running, Succeeded, WaitingApproval} {
		err := s.Write(ctx, func(tx *sql.Tx) error { _, err := Transition(ctx, tx, tk.ID, to, "", "", "core"); return err })
		if !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("CREATED -> %s allowed: %v", to, err)
		}
	}
	s.Write(ctx, func(tx *sql.Tx) error { _, err := Transition(ctx, tx, tk.ID, Cancelled, "x", "", "owner"); return err })
	err := s.Write(ctx, func(tx *sql.Tx) error { _, err := Transition(ctx, tx, tk.ID, Planning, "", "", "core"); return err })
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatal("terminal task moved")
	}
}

func TestEventDataRedacted(t *testing.T) {
	obs.RegisterSecret("tok-very-secret-1234")
	s := newStore(t)
	ctx := context.Background()
	tk := create(t, s)
	s.Write(ctx, func(tx *sql.Tx) error {
		_, err := AppendEvent(ctx, tx, tk.ID, nil, "X", "core", map[string]any{"v": "tok-very-secret-1234"})
		return err
	})
	evs, _ := Events(ctx, s.R, tk.ID, 1, true)
	if string(evs[0].Data) != `{"v":"[REDACTED]"}` {
		t.Fatal(string(evs[0].Data))
	}
}

func TestStepsAndRunnable(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tk := create(t, s)
	tool := "web.fetch"
	steps := []Step{{StepNo: 1, Description: "a", ToolID: &tool, SideEffect: "read"}, {StepNo: 2, Description: "b", SideEffect: "pure"}}
	if err := s.Write(ctx, func(tx *sql.Tx) error { return InsertSteps(ctx, tx, tk.ID, steps) }); err != nil {
		t.Fatal(err)
	}
	got, _ := Steps(ctx, s.R, tk.ID)
	if len(got) != 2 || got[0].IdempotencyKey != StepKey(tk.ID, 1) {
		t.Fatalf("%+v", got)
	}
	s.Write(ctx, func(tx *sql.Tx) error {
		return UpdateStep(ctx, tx, got[0], StepStarted, "", "", "", "STEP_STARTED", nil)
	})
	again, _ := GetStep(ctx, s.R, got[0].ID)
	if again.Attempts != 1 || again.State != StepStarted {
		t.Fatalf("%+v", again)
	}
	r, _ := Runnable(ctx, s.R)
	if r == nil || r.ID != tk.ID {
		t.Fatal("not runnable")
	}
}
