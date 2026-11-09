package budget

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

func setup(t *testing.T, daily float64) (*Governor, *store.Store, string) {
	t.Helper()
	d := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tk := &task.Task{Objective: "x", ConfigHash: "h"}
	s.Write(context.Background(), func(tx *sql.Tx) error { return task.Insert(context.Background(), tx, tk) })
	return New(s, daily), s, tk.ID
}

func call(taskID string, est, limit float64) Call {
	return Call{TaskID: taskID, Tier: "standard", ProviderID: "openrouter", Route: "r", APIModelID: "m", EstimateUSD: est, TaskLimitUSD: limit}
}

// 50 concurrent reservations never exceed the task limit (D.1 P1 budget test).
func TestConcurrentReservationsNeverOverspend(t *testing.T) {
	g, s, id := setup(t, 100)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, denied := 0, 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := g.Reserve(context.Background(), call(id, 0.03, 0.50))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrTaskBudget):
				denied++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var total float64
	s.R.QueryRow("select sum(cost_reserved_usd) from usage_records").Scan(&total)
	if ok != 16 || denied != 34 || total > 0.50+1e-9 {
		t.Fatalf("ok=%d denied=%d total=%.4f", ok, denied, total)
	}
}

func TestDailyLimitAcrossTasks(t *testing.T) {
	g, s, id := setup(t, 0.10)
	tk2 := &task.Task{Objective: "y", ConfigHash: "h"}
	s.Write(context.Background(), func(tx *sql.Tx) error { return task.Insert(context.Background(), tx, tk2) })
	if _, err := g.Reserve(context.Background(), call(id, 0.08, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Reserve(context.Background(), call(tk2.ID, 0.05, 1)); !errors.Is(err, ErrDailyBudget) {
		t.Fatalf("daily limit not enforced: %v", err)
	}
}

func TestReconcileReleasesAndUnknownCostFailsClosed(t *testing.T) {
	g, s, id := setup(t, 10)
	ctx := context.Background()
	r, _ := g.Reserve(ctx, call(id, 0.10, 1))
	cost := 0.02
	g.Reconcile(ctx, r, id, Actual{CostUSD: &cost, Outcome: "ok"})
	r2, _ := g.Reserve(ctx, call(id, 0.10, 1))
	g.Reconcile(ctx, r2, id, Actual{Outcome: "error"})
	tk, _ := task.Get(ctx, s.R, id)
	if tk.CostReservedUSD > 1e-9 || tk.CostUSD < 0.12-1e-9 || tk.CostUSD > 0.12+1e-9 {
		t.Fatalf("reserved=%v actual=%v", tk.CostReservedUSD, tk.CostUSD)
	}
	var unc int
	s.R.QueryRow("select cost_uncertain from usage_records where id=?", r2.UsageID).Scan(&unc)
	if unc != 1 {
		t.Fatal("unknown cost not flagged")
	}
}

func TestIterationLimitAndHalt(t *testing.T) {
	g, _, id := setup(t, 10)
	c := call(id, 0.01, 1)
	c.MaxCalls = 2
	g.Reserve(context.Background(), c)
	g.Reserve(context.Background(), c)
	if _, err := g.Reserve(context.Background(), c); !errors.Is(err, ErrIterations) {
		t.Fatalf("iterations: %v", err)
	}
	g.Halt()
	if _, err := g.Reserve(context.Background(), call(id, 0.01, 1)); !errors.Is(err, ErrHalted) {
		t.Fatal("halt not enforced")
	}
	if _, err := g.Reserve(context.Background(), call(id, 0, 1)); err == nil {
		t.Fatal("zero estimate accepted")
	}
}
