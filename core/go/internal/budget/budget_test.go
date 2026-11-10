package budget

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	// Calls the provider rejected outright (429, 402) and empty answers (always
	// retried) are not iterations.
	for i := 0; i < 5; i++ {
		r, _ := g.Reserve(context.Background(), c)
		zero := 0.0
		g.Reconcile(context.Background(), r, id, Actual{CostUSD: &zero, Outcome: []string{"rejected", "empty_output"}[i%2]})
	}
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

// OL-08: the daily budget window follows the workspace time zone (WIB), not UTC.
// 18:00 UTC on 9 Nov is 01:00 WIB on 10 Nov: a call made at 23:59 WIB on the 9th
// (16:59 UTC) belongs to the previous workspace day.
func TestDailyWindowUsesWorkspaceTimeZone(t *testing.T) {
	g, _, id := setup(t, 0.10)
	wib, _ := time.LoadLocation("Asia/Jakarta")
	g.SetLocation(wib)
	ctx := context.Background()
	at := func(utc string) {
		ts, _ := time.Parse(time.RFC3339, utc)
		g.SetClockForTest(func() time.Time { return ts })
	}
	at("2026-11-09T16:59:00Z") // 23:59 WIB, 9 Nov
	if _, err := g.Reserve(ctx, call(id, 0.08, 1)); err != nil {
		t.Fatal(err)
	}
	at("2026-11-09T17:01:00Z") // 00:01 WIB, 10 Nov: a new workspace day
	if _, err := g.Reserve(ctx, call(id, 0.08, 1)); err != nil {
		t.Fatalf("new WIB day should have a fresh budget: %v", err)
	}
	at("2026-11-09T18:00:00Z") // 01:00 WIB, still 10 Nov (and still 9 Nov in UTC): both calls count
	if _, err := g.Reserve(ctx, call(id, 0.08, 1)); err == nil {
		t.Fatal("daily limit ignored inside the same WIB day")
	}
}
