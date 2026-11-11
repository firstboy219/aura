package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/task"
	"aura.local/core/internal/view"
)

// Reminders and schedules from conversation (ADR-0063). A reply's "schedule"
// block names what to remind (or do) and when; the Core keeps it. A reminder
// only ever becomes a notification to the owner. A daily "task" schedule is an
// ordinary task trigger: each run is a normal task under the same grant,
// budget and approval rules as if the owner had asked again.

// Reminder is one reminder of the owner.
type Reminder struct {
	ID          string  `json:"id"`
	Text        string  `json:"text"`
	DueAt       *int64  `json:"due_at"`
	DailyAt     *string `json:"daily_at"`
	NextAt      int64   `json:"next_at"`
	Enabled     bool    `json:"enabled"`
	TaskID      *string `json:"task_id"`
	LastFiredAt *int64  `json:"last_fired_at"`
}

var (
	reWhenAt    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`)
	reWhenDaily = regexp.MustCompile(`^daily (\d{2}:\d{2})$`)
)

// localNow is the current time in the workspace time zone, written for a
// model (Blueprint OL-08: "today" is never UTC by accident).
func (e *Engine) localNow() string {
	days := []string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	n := time.Now().In(e.cfg.Location())
	return fmt.Sprintf("%s, %s (%s)", days[n.Weekday()], n.Format("2006-01-02 15:04"), e.cfg.Location().String())
}

// parseWhen reads a schedule time: "2026-11-11T15:00" (once, workspace time
// zone) or "daily 06:00". A one-shot time must lie in the future.
func parseWhen(when string, loc *time.Location, now time.Time) (due *int64, daily *string, next int64, err error) {
	when = strings.TrimSpace(when)
	if m := reWhenDaily.FindStringSubmatch(when); m != nil {
		d := m[1]
		n, err := nextRun(nil, &d, now, loc)
		if err != nil {
			return nil, nil, 0, err
		}
		return nil, &d, n.UnixMilli(), nil
	}
	if reWhenAt.MatchString(when) {
		t, err := time.ParseInLocation("2006-01-02T15:04", when, loc)
		if err != nil {
			return nil, nil, 0, err
		}
		if !t.After(now) {
			return nil, nil, 0, errors.New("time is in the past")
		}
		ms := t.UnixMilli()
		return &ms, nil, ms, nil
	}
	return nil, nil, 0, fmt.Errorf("when must be YYYY-MM-DDTHH:MM or \"daily HH:MM\", not %q", when)
}

type pendingSchedule struct {
	text, dailyAt string
}

// saveSchedules keeps the reminders of a reply's schedule blocks, once per
// task, and returns the daily task schedules to create after the reply is
// stored (a trigger is created in its own transaction).
func (e *Engine) saveSchedules(ctx context.Context, tx *sql.Tx, t *task.Task, blocks []view.Block) (int, []pendingSchedule, error) {
	loc, now := e.cfg.Location(), time.Now()
	seq, kept := 0, 0
	var later []pendingSchedule
	for _, b := range blocks {
		if b.Type != "schedule" {
			continue
		}
		for _, it := range b.Items {
			seq++
			due, daily, next, err := parseWhen(it.Value, loc, now)
			if err != nil {
				continue // the sanitizer checked the form; a time already past is skipped
			}
			if it.Note == "task" && daily != nil {
				later = append(later, pendingSchedule{text: it.Label, dailyAt: *daily})
				continue
			}
			ms := ids.NowMs()
			res, err := tx.ExecContext(ctx, `insert into reminders(id, text, due_at, daily_at, next_at, task_id, seq, created_at, updated_at)
				values(?,?,?,?,?,?,?,?,?) on conflict(task_id, seq) where task_id is not null do nothing`,
				ids.New(), it.Label, due, daily, next, t.ID, seq, ms, ms)
			if err != nil {
				return 0, nil, err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				kept++
			}
		}
	}
	return kept, later, nil
}

// createSchedules turns daily task schedules into task triggers, skipping one
// that already exists with the same request and time (a reply stored again).
func (e *Engine) createSchedules(ctx context.Context, t *task.Task, list []pendingSchedule) {
	for _, p := range list {
		var n int
		e.st.R.QueryRowContext(ctx, "select count(*) from triggers where kind='task' and objective=? and daily_at=?", p.text, p.dailyAt).Scan(&n)
		if n > 0 {
			continue
		}
		name := truncate("Jadwal: "+p.text, 64) + " #" + strings.ToLower(ids.New()[20:])
		d := p.dailyAt
		if _, err := e.CreateTrigger(ctx, &Trigger{Name: name, Kind: "task", DailyAt: &d, Objective: p.text,
			Spec: TriggerSpec{DataClass: t.Scope.DataClass}}); err != nil {
			e.log.Warn("schedule from reply not created", "task_id", t.ID, "err", err)
		}
	}
}

// RunReminders turns due reminders into notifications. A daily reminder moves
// to its next day; a one-shot reminder is switched off. Each slot is claimed
// first, so a crash never notifies the same slot twice.
func (e *Engine) RunReminders(ctx context.Context) {
	now := time.Now()
	rows, err := e.st.R.QueryContext(ctx, "select id, text, daily_at, next_at from reminders where enabled=1 and next_at<=? limit 50", now.UnixMilli())
	if err != nil {
		return
	}
	type due struct {
		id, text string
		daily    *string
		at       int64
	}
	var list []due
	for rows.Next() {
		var d due
		if rows.Scan(&d.id, &d.text, &d.daily, &d.at) == nil {
			list = append(list, d)
		}
	}
	rows.Close()
	for _, d := range list {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			var res sql.Result
			var err error
			if d.daily != nil {
				next, _ := nextRun(nil, d.daily, now, e.cfg.Location())
				res, err = tx.ExecContext(ctx, "update reminders set next_at=?, last_fired_at=?, updated_at=? where id=? and next_at=? and enabled=1",
					next.UnixMilli(), now.UnixMilli(), now.UnixMilli(), d.id, d.at)
			} else {
				res, err = tx.ExecContext(ctx, "update reminders set enabled=0, last_fired_at=?, updated_at=? where id=? and next_at=? and enabled=1",
					now.UnixMilli(), now.UnixMilli(), d.id, d.at)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return nil // claimed by another run
			}
			body, _ := json.Marshal(map[string]any{"reminder_id": d.id, "text": d.text, "due_at": d.at})
			_, err = tx.ExecContext(ctx, "insert into notifications(id, kind, title, body_json, created_at) values(?,?,?,?,?)",
				ids.New(), "info", truncate("⏰ "+d.text, 200), string(body), now.UnixMilli())
			return err
		})
	}
}

// Reminders lists the owner's reminders, active first and soonest first.
func (e *Engine) Reminders(ctx context.Context) ([]Reminder, error) {
	rows, err := e.st.R.QueryContext(ctx, `select id, text, due_at, daily_at, next_at, enabled, task_id, last_fired_at from reminders
		order by enabled desc, next_at limit 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reminder{}
	for rows.Next() {
		var r Reminder
		var en int
		if err := rows.Scan(&r.ID, &r.Text, &r.DueAt, &r.DailyAt, &r.NextAt, &en, &r.TaskID, &r.LastFiredAt); err != nil {
			return nil, err
		}
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteReminder removes a reminder.
func (e *Engine) DeleteReminder(ctx context.Context, id string) error {
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "delete from reminders where id=?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return nil
	})
}
