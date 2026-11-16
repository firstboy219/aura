package policy

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"testing"
	"time"
)

// ODI A9 load check: many concurrent audited writes (each reads the chain head inside its write transaction) while readers hammer the database. The single-writer
// queue must serialize them without SQLITE_BUSY, the chain must stay valid and gap-free, and the tail latency must not blow up. Numbers are logged for the report.
func TestAuditedWritesUnderContentionKeepTheChainValid(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	s, _ := setup(t)
	ctx := context.Background()
	const writers, each = 12, 150
	var mu sync.Mutex
	var lat []time.Duration
	var errs int
	stop := make(chan struct{})
	var rd sync.WaitGroup
	reads := 0
	for r := 0; r < 4; r++ {
		rd.Add(1)
		go func() {
			defer rd.Done()
			for {
				select {
				case <-stop:
					return
				default:
					var n int
					s.R.QueryRow("select count(*) from audit_events").Scan(&n)
					mu.Lock()
					reads++
					mu.Unlock()
				}
			}
		}()
	}
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				t0 := time.Now()
				err := s.Write(ctx, func(tx *sql.Tx) error {
					return Audit(ctx, tx, "LOAD", "load", "", "", map[string]any{"w": w, "i": i})
				})
				d := time.Since(t0)
				mu.Lock()
				lat = append(lat, d)
				if err != nil {
					errs++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	total := time.Since(start)
	close(stop)
	rd.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration { return lat[int(float64(len(lat)-1)*q)] }
	t.Logf("%d audited writes by %d writers + 4 readers (%d reads): %v total, %.0f writes/s, p50 %v p95 %v p99 %v max %v, errors %d",
		len(lat), writers, reads, total.Round(time.Millisecond), float64(len(lat))/total.Seconds(), p(.5).Round(time.Microsecond), p(.95).Round(time.Microsecond), p(.99).Round(time.Microsecond), lat[len(lat)-1].Round(time.Microsecond), errs)
	if errs != 0 {
		t.Fatalf("%d writes failed under contention", errs)
	}
	st, err := VerifyAuditChain(ctx, s.R)
	if err != nil || !st.Valid || st.Chained != writers*each {
		t.Fatalf("chain after the load: %+v %v", st, err)
	}
	if p(.99) > 3*time.Second {
		t.Fatalf("p99 write latency %v is pathological", p(.99))
	}
}
