package engine

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"

	"aura.local/core/internal/task"
)

// TestMeasureStepOverhead (opt-in: AURA_MEASURE=1) runs RS-1 repeatedly with a
// zero-latency fake provider and a local fixture server, so step durations are
// Core + worker overhead without provider/network latency (Blueprint §12.1).
func TestMeasureStepOverhead(t *testing.T) {
	if os.Getenv("AURA_MEASURE") != "1" {
		t.Skip("set AURA_MEASURE=1")
	}
	h := newHarness(t, harnessOpts{})
	const runs = 20
	var steps, tasks []time.Duration
	var maxWorkerRSS int64
	for i := 0; i < runs; i++ {
		tk := h.submit(rs1Objective(h.host), nil)
		start := time.Now()
		for {
			cur, _ := task.Get(context.Background(), h.st.R, tk.ID)
			if rss := h.sup.RSS(); rss > maxWorkerRSS {
				maxWorkerRSS = rss
			}
			if cur.State.Terminal() {
				if cur.State != task.Succeeded {
					t.Fatalf("run %d: %s", i, cur.State)
				}
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		tasks = append(tasks, time.Since(start))
		started := map[string]int64{}
		for _, e := range h.events(tk.ID) {
			if e.StepID == nil {
				continue
			}
			switch e.Type {
			case "STEP_STARTED":
				started[*e.StepID] = e.At
			case "STEP_SUCCEEDED":
				steps = append(steps, time.Duration(e.At-started[*e.StepID])*time.Millisecond)
			}
		}
	}
	pct := func(d []time.Duration, p float64) time.Duration {
		s := append([]time.Duration(nil), d...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[int(float64(len(s)-1)*p)]
	}
	t.Logf("step overhead (n=%d): p50=%v p95=%v p99=%v max=%v", len(steps), pct(steps, .5), pct(steps, .95), pct(steps, .99), pct(steps, 1))
	t.Logf("RS-1 task wall time (n=%d, 4 steps + plan): p50=%v p95=%v", len(tasks), pct(tasks, .5), pct(tasks, .95))
	t.Logf("worker max RSS observed: %d MiB", maxWorkerRSS>>20)
}
