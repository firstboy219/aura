package engine

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

func TestParseWhen(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, loc)
	due, daily, next, err := parseWhen("2026-11-11T15:00", loc, now)
	if err != nil || daily != nil || *due != time.Date(2026, 11, 11, 15, 0, 0, 0, loc).UnixMilli() || next != *due {
		t.Fatalf("once: %v %v %v", due, daily, err)
	}
	_, daily, next, err = parseWhen("daily 06:00", loc, now)
	if err != nil || *daily != "06:00" || next != time.Date(2026, 11, 12, 6, 0, 0, 0, loc).UnixMilli() {
		t.Fatalf("daily: %v %v", daily, err)
	}
	for _, bad := range []string{"2026-11-11T09:00", "besok jam 7", "daily 25:00", ""} {
		if _, _, _, err := parseWhen(bad, loc, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// ADR-0063: a reply's schedule block becomes reminders (kept once) and daily
// task schedules; a due reminder becomes exactly one notification.
func TestReplySchedulesRemindersAndDailyTasks(t *testing.T) {
	var loc *time.Location // set once the harness exists; the model is asked only after that
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(func(string) string { return noSteps },
		func(string) gatewaytest.Reply {
			return gatewaytest.Reply{Content: replyJSON("Baik, Tuan.", nullBlock("schedule", map[string]any{"items": []any{
				nullItem("minum obat", time.Now().In(loc).Add(2*time.Hour).Format("2006-01-02T15:04"), ""),
				map[string]any{"label": "Cek berita hari ini dan ringkas untuk saya", "value": "daily 05:45", "note": "task", "url": nil},
				nullItem("waktu lampau", "2020-01-01T08:00", ""),
				nullItem("tanpa waktu", "nanti sore", ""),
			}}))}
		})})
	loc = h.cfg.Location()
	ctx := context.Background()
	tk, _, err := h.eng.Submit(ctx, CreateInput{Objective: "Ingatkan saya minum obat dan setiap pagi kirim ringkasan berita", AllowDomains: []string{h.host}})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	rs, _ := h.eng.Reminders(ctx)
	if len(rs) != 1 || rs[0].Text != "minum obat" || rs[0].DueAt == nil || !rs[0].Enabled {
		t.Fatalf("reminders: %+v", rs)
	}
	trs, _ := h.eng.ListTriggers(ctx)
	if len(trs) != 1 || trs[0].Kind != "task" || *trs[0].DailyAt != "05:45" || trs[0].Objective != "Cek berita hari ini dan ringkas untuk saya" {
		t.Fatalf("triggers: %+v", trs)
	}
	// Storing the same reply again (recovery) adds nothing.
	r, _ := h.eng.GetReply(ctx, tk.ID)
	full, _ := task.Get(ctx, h.st.R, tk.ID)
	if err := h.eng.storeReply(ctx, full, r.Text, r.View, r.Scene, r.Basis, nil, nil); err != nil {
		t.Fatal(err)
	}
	rs2, _ := h.eng.Reminders(ctx)
	trs2, _ := h.eng.ListTriggers(ctx)
	if len(rs2) != 1 || len(trs2) != 1 {
		t.Fatalf("duplicates: %d reminders, %d triggers", len(rs2), len(trs2))
	}
	// Due now: one notification, then the one-shot reminder is off; a second run notifies nothing.
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update reminders set next_at=?", time.Now().Add(-time.Minute).UnixMilli())
		return err
	})
	h.eng.RunReminders(ctx)
	h.eng.RunReminders(ctx)
	ns, _ := h.eng.Notifications(ctx, false)
	count := 0
	for _, n := range ns {
		if strings.Contains(n.Title, "minum obat") {
			count++
		}
	}
	rs3, _ := h.eng.Reminders(ctx)
	if count != 1 || rs3[0].Enabled || rs3[0].LastFiredAt == nil {
		t.Fatalf("notifications %d, reminder %+v", count, rs3[0])
	}
	if err := h.eng.DeleteReminder(ctx, rs3[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.DeleteReminder(ctx, rs3[0].ID); err == nil {
		t.Fatal("deleted twice")
	}
}
