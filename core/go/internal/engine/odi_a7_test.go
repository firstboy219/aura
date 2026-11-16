package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/task"
)

func TestQuietWindow(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	at := func(h, m int) time.Time { return time.Date(2026, 11, 16, h, m, 0, 0, loc) }
	for _, c := range []struct {
		name     string
		now      time.Time
		from, to string
		quiet    bool
		endsAt   time.Time
	}{
		{"evening part of an overnight window", at(23, 30), "22:00", "06:00", true, time.Date(2026, 11, 17, 6, 0, 0, 0, loc)},
		{"morning part of an overnight window", at(5, 59), "22:00", "06:00", true, at(6, 0)},
		{"exactly at the end is awake", at(6, 0), "22:00", "06:00", false, time.Time{}},
		{"exactly at the start is quiet", at(22, 0), "22:00", "06:00", true, time.Date(2026, 11, 17, 6, 0, 0, 0, loc)},
		{"daytime is awake", at(12, 0), "22:00", "06:00", false, time.Time{}},
		{"same-day window", at(13, 15), "13:00", "14:00", true, at(14, 0)},
		{"outside a same-day window", at(14, 0), "13:00", "14:00", false, time.Time{}},
		{"no window", at(3, 0), "", "", false, time.Time{}},
	} {
		end, q := quietEnd(c.now, c.from, c.to, loc)
		if q != c.quiet || (q && !end.Equal(c.endsAt)) {
			t.Errorf("%s: quiet=%v end=%v, want %v / %v", c.name, q, end, c.quiet, c.endsAt)
		}
	}
}

func TestTriggerPolicyValidation(t *testing.T) {
	for name, p := range map[string]*TriggerPolicy{
		"bad overlap":       {Overlap: "queue"},
		"half a window":     {QuietFrom: "22:00"},
		"bad time":          {QuietFrom: "late", QuietTo: "06:00"},
		"empty window":      {QuietFrom: "22:00", QuietTo: "22:00"},
		"negative expiry":   {ExpiresAt: -1},
		"huge jitter":       {JitterSeconds: 99999},
		"max_runs on watch": {MaxRuns: 3},
	} {
		kind := "task"
		if name == "max_runs on watch" {
			kind = "watch"
		}
		if p.validate(kind) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if (&TriggerPolicy{Overlap: "skip", QuietFrom: "22:00", QuietTo: "06:00", MaxRuns: 5, MissedGraceSeconds: 120, JitterSeconds: 30}).validate("task") != nil {
		t.Fatal("a valid policy was refused")
	}
	var nilPolicy *TriggerPolicy
	if nilPolicy.validate("task") != nil {
		t.Fatal("no policy must be valid")
	}
}

func newPolicyTrigger(t *testing.T, h *harness, name string, p *TriggerPolicy) *Trigger {
	t.Helper()
	iv := int64(60)
	tr, err := h.eng.CreateTrigger(context.Background(), &Trigger{Name: name, Kind: "task", IntervalSeconds: &iv, Objective: rs1Objective(h.host),
		Spec: TriggerSpec{Tools: []string{"web.fetch", "workspace.write"}, AllowDomains: []string{h.host}, DataClass: "public", Policy: p}})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func countAudit(h *harness, typ string) int {
	var n int
	h.st.R.QueryRow("select count(*) from audit_events where type=?", typ).Scan(&n)
	return n
}

func countTriggerTasks(h *harness, id string) int {
	var n int
	h.st.R.QueryRow("select count(*) from tasks where idempotency_key like ?", "trigger:"+id+":%").Scan(&n)
	return n
}

func makeDue(h *harness, id string, agoMs int64) {
	h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("update triggers set next_run_at=? where id=?", time.Now().UnixMilli()-agoMs, id)
		return err
	})
}

func TestTriggerOverlapSkipAndExpiryAndMaxRuns(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true}) // nothing executes: a started task stays "still going"
	ctx := context.Background()
	tr := newPolicyTrigger(t, h, "overlap", &TriggerPolicy{Overlap: "skip"})
	makeDue(h, tr.ID, 0)
	h.eng.RunTriggers(ctx)
	if countTriggerTasks(h, tr.ID) != 1 {
		t.Fatalf("first slot did not run")
	}
	makeDue(h, tr.ID, 0)
	h.eng.RunTriggers(ctx)
	if n := countTriggerTasks(h, tr.ID); n != 1 || countAudit(h, "TRIGGER_SKIPPED") != 1 {
		t.Fatalf("overlap: tasks=%d skipped audit=%d", n, countAudit(h, "TRIGGER_SKIPPED"))
	}
	var next int64
	h.st.R.QueryRow("select next_run_at from triggers where id=?", tr.ID).Scan(&next)
	if next <= time.Now().UnixMilli() {
		t.Fatal("a skipped slot must advance the schedule")
	}
	// the previous run finishes: the next slot runs
	var tid string
	h.st.R.QueryRow("select id from tasks where idempotency_key like ?", "trigger:"+tr.ID+":%").Scan(&tid)
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update tasks set state=? where id=?", string(task.Failed), tid)
		return err
	})
	makeDue(h, tr.ID, 0)
	h.eng.RunTriggers(ctx)
	if countTriggerTasks(h, tr.ID) != 2 {
		t.Fatal("after the previous run ended the slot did not run")
	}

	// expiry
	ex := newPolicyTrigger(t, h, "expiring", &TriggerPolicy{ExpiresAt: time.Now().Add(-time.Hour).UnixMilli()})
	makeDue(h, ex.ID, 0)
	h.eng.RunTriggers(ctx)
	var enabled int
	h.st.R.QueryRow("select enabled from triggers where id=?", ex.ID).Scan(&enabled)
	if enabled != 0 || countTriggerTasks(h, ex.ID) != 0 || countAudit(h, "TRIGGER_EXPIRED") != 1 {
		t.Fatalf("expired trigger: enabled=%d tasks=%d", enabled, countTriggerTasks(h, ex.ID))
	}

	// max runs
	mr := newPolicyTrigger(t, h, "twice", &TriggerPolicy{MaxRuns: 1})
	makeDue(h, mr.ID, 0)
	h.eng.RunTriggers(ctx)
	makeDue(h, mr.ID, 0)
	h.eng.RunTriggers(ctx)
	h.st.R.QueryRow("select enabled from triggers where id=?", mr.ID).Scan(&enabled)
	if countTriggerTasks(h, mr.ID) != 1 || enabled != 0 || countAudit(h, "TRIGGER_COMPLETED") != 1 {
		t.Fatalf("max_runs: tasks=%d enabled=%d", countTriggerTasks(h, mr.ID), enabled)
	}
}

func TestTriggerMissedGraceAndQuietHoursNeverBurst(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	// a slot missed by an hour with a one-minute grace is skipped, not run late
	late := newPolicyTrigger(t, h, "late", &TriggerPolicy{MissedGraceSeconds: 60})
	makeDue(h, late.ID, 3600_000)
	h.eng.RunTriggers(ctx)
	if countTriggerTasks(h, late.ID) != 0 || !strings.Contains(auditReasons(h, "TRIGGER_SKIPPED"), "missed") {
		t.Fatalf("a long-missed slot ran or was not audited (%d tasks)", countTriggerTasks(h, late.ID))
	}
	var next int64
	h.st.R.QueryRow("select next_run_at from triggers where id=?", late.ID).Scan(&next)
	if next <= time.Now().UnixMilli() {
		t.Fatal("no catch-up burst: the next slot must be in the future")
	}
	// without a policy, a missed slot runs ONCE (the behaviour AURA always had), never a burst
	plain := newPolicyTrigger(t, h, "plain", nil)
	makeDue(h, plain.ID, 5*3600_000)
	h.eng.RunTriggers(ctx)
	h.eng.RunTriggers(ctx)
	if n := countTriggerTasks(h, plain.ID); n != 1 {
		t.Fatalf("a missed slot ran %d times", n)
	}
	// quiet hours: the slot waits for the end of the window and does not run inside it
	loc := h.eng.cfg.Location()
	now := time.Now().In(loc)
	q := newPolicyTrigger(t, h, "quiet", &TriggerPolicy{QuietFrom: now.Add(-time.Hour).Format("15:04"), QuietTo: now.Add(time.Hour).Format("15:04")})
	makeDue(h, q.ID, 0)
	h.eng.RunTriggers(ctx)
	h.st.R.QueryRow("select next_run_at from triggers where id=?", q.ID).Scan(&next)
	if countTriggerTasks(h, q.ID) != 0 || next < now.Add(30*time.Minute).UnixMilli() {
		t.Fatalf("quiet hours: tasks=%d next in %v", countTriggerTasks(h, q.ID), time.UnixMilli(next).Sub(now))
	}
}

func auditReasons(h *harness, typ string) string {
	rows, _ := h.st.R.Query("select data_json from audit_events where type=?", typ)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return strings.Join(out, "|")
}

func TestMemoryExpiryRetiresWithoutErasing(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	m, err := h.eng.AddOwnerMemory(ctx, "The office printer is called Gandalf", false)
	if err != nil {
		t.Fatal(err)
	}
	m, err = h.eng.SetMemoryExpiry(ctx, m.ID, 30)
	if err != nil || m.ExpiresAt == nil {
		t.Fatalf("%v %+v", err, m)
	}
	if found, _ := h.eng.SearchMemories(ctx, "printer Gandalf", true, 5); len(found) != 1 {
		t.Fatalf("an unexpired memory was not found: %d", len(found))
	}
	if _, err := h.eng.SetMemoryExpiry(ctx, m.ID, 99999); err == nil {
		t.Fatal("an absurd expiry was accepted")
	}
	// time passes
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update memories set expires_at=? where id=?", time.Now().Add(-time.Minute).UnixMilli(), m.ID)
		return err
	})
	if found, _ := h.eng.SearchMemories(ctx, "printer Gandalf", true, 5); len(found) != 0 {
		t.Fatal("an expired memory is still retrieved as a fact (before the tick has even run)")
	}
	h.eng.ExpireMemories(ctx)
	got, err := h.eng.getMemory(ctx, m.ID)
	if err != nil || got.Status != "deleted" || got.Content == "" {
		t.Fatalf("the expired memory should be retired, not erased: %v %+v", err, got)
	}
	if countAudit(h, "MEMORY_EXPIRED") != 1 || countAudit(h, "MEMORY_EXPIRY_SET") != 1 {
		t.Fatalf("audit expired=%d set=%d", countAudit(h, "MEMORY_EXPIRED"), countAudit(h, "MEMORY_EXPIRY_SET"))
	}
	if _, err := h.eng.SetMemoryExpiry(ctx, m.ID, 5); err == nil {
		t.Fatal("a retired memory cannot be given a new expiry")
	}
}

func insertNotification(h *harness, id, title string, at int64) {
	h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("insert into notifications(id, kind, title, body_json, created_at) values(?, 'info', ?, '{}', ?)", id, title, at)
		return err
	})
}

func TestNotificationsDedupSnoozeAckExpire(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	now := time.Now().UnixMilli()
	insertNotification(h, "n1", "Disk is 90% full", now)
	insertNotification(h, "n2", "Disk is 90% full", now+1000) // same alert, still unread: dropped
	insertNotification(h, "n3", "Backup failed", now)
	ns, _ := h.eng.Notifications(ctx, true)
	if len(ns) != 2 {
		t.Fatalf("a duplicate unread alert was kept: %d notifications", len(ns))
	}
	// once read, the same alert can come again
	if err := h.eng.MarkRead(ctx, "n1"); err != nil {
		t.Fatal(err)
	}
	insertNotification(h, "n4", "Disk is 90% full", now+2000)
	if ns, _ = h.eng.Notifications(ctx, true); len(ns) != 2 {
		t.Fatalf("after reading, a new alert must be allowed: %d", len(ns))
	}
	// snooze hides it for a while; an out-of-range snooze is refused
	if err := h.eng.SnoozeNotification(ctx, "n3", 0); err == nil {
		t.Fatal("snooze of 0 minutes accepted")
	}
	if err := h.eng.SnoozeNotification(ctx, "n3", 60); err != nil {
		t.Fatal(err)
	}
	if ns, _ = h.eng.Notifications(ctx, true); len(ns) != 1 {
		t.Fatalf("a snoozed notification is still listed: %d", len(ns))
	}
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update notifications set snoozed_until=? where id='n3'", now-1)
		return err
	})
	if ns, _ = h.eng.Notifications(ctx, true); len(ns) != 2 {
		t.Fatalf("a notification must come back when its snooze ends: %d", len(ns))
	}
	// acknowledge: read, with the acknowledgement recorded
	if err := h.eng.AckNotification(ctx, "n3"); err != nil {
		t.Fatal(err)
	}
	var read, acked sql.NullInt64
	h.st.R.QueryRow("select read_at, acked_at from notifications where id='n3'").Scan(&read, &acked)
	if !read.Valid || !acked.Valid {
		t.Fatalf("ack: read=%v acked=%v", read, acked)
	}
	if err := h.eng.AckNotification(ctx, "n3"); err == nil {
		t.Fatal("a second acknowledgement should not match")
	}
	// expiry hides a notification
	insertNotification(h, "n5", "Old news", now)
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update notifications set expires_at=? where id='n5'", now-1)
		return err
	})
	for _, n := range func() []Notification { l, _ := h.eng.Notifications(ctx, false); return l }() {
		if n.ID == "n5" {
			t.Fatal("an expired notification is listed")
		}
	}
}

func TestRetentionReportIsADryRun(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	old := time.Now().Add(-200 * 24 * time.Hour).UnixMilli()
	insertNotification(h, "r1", "old and read", old)
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update notifications set read_at=? where id='r1'", old)
		if err != nil {
			return err
		}
		_, err = tx.Exec("insert into settings(key, value, updated_at) values(?,?,1)", RetentionSetting, `{"read_notifications_days":90}`)
		return err
	})
	p, would, err := h.eng.RetentionReport(ctx, h.st.R)
	if err != nil || p.ReadNotificationsDays != 90 || would["read_notifications"] != 1 || would["shell_log"] != 0 {
		t.Fatalf("%v %+v %v", err, p, would)
	}
	var n int
	h.st.R.QueryRow("select count(*) from notifications where id='r1'").Scan(&n)
	if n != 1 {
		t.Fatal("the dry-run report deleted data")
	}
}
