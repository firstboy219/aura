package engine

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"aura.local/core/internal/task"
)

func count(h *harness, q string, args ...any) int {
	var n int
	h.st.R.QueryRow(q, args...).Scan(&n)
	return n
}

func setRetention(h *harness, json string) {
	h.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("insert into settings(key, value, updated_at) values(?,?,1) on conflict(key) do update set value=excluded.value", RetentionSetting, json)
		return err
	})
}

func TestRetentionNumbersBelowTheFloorsAreRefused(t *testing.T) {
	for _, p := range []RetentionPolicy{{ReadNotificationsDays: 1}, {ShellLogDays: 3}, {RetiredMemoriesDays: 10}, {ConsumedApprovalsDays: 29}, {ReadNotificationsDays: -5}} {
		if err := ValidateRetention(p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted", p)
		}
	}
	if err := ValidateRetention(RetentionPolicy{ReadNotificationsDays: 30, ShellLogDays: 30, RetiredMemoriesDays: 90, ConsumedApprovalsDays: 90, Enforce: true}); err != nil {
		t.Fatalf("the recommended numbers: %v", err)
	}
	if err := ValidateRetention(RetentionPolicy{}); err != nil {
		t.Fatalf("0 keeps forever and is allowed: %v", err)
	}
}

// Enforcement deletes exactly what the numbers allow, never the audit log, never what is still referenced, and says so in one audit entry without content.
func TestPurgeRetentionDeletesOnlyWhatTheNumbersAllow(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	ctx := context.Background()
	day := int64(24 * time.Hour / time.Millisecond)
	now := time.Now().UnixMilli()
	old, fresh := now-200*day, now-2*day
	tid := seedTasks(t, h, "web.fetch", 1, task.Succeeded, "", false)[0]

	if n, err := h.eng.PurgeRetention(ctx); n != nil || err != nil {
		t.Fatalf("no policy: nothing happens %v %v", n, err)
	}
	insertNotification(h, "n-old", "old read", old)
	insertNotification(h, "n-unread-old", "old unread", old)
	insertNotification(h, "n-new", "recent read", fresh)
	err := h.st.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			"update notifications set read_at=created_at where id in ('n-old','n-new')",
			"insert into memories(id,kind,content,privacy,status,workspace_id,created_at,updated_at) values('m-del','fact','x','public','deleted','w',1,1)",
			"insert into memories(id,kind,content,privacy,status,workspace_id,created_at,updated_at) values('m-sup','fact','x','public','superseded','w',1,1)",
			"insert into memories(id,kind,content,privacy,status,workspace_id,created_at,updated_at) values('m-keep','fact','x','public','active','w',1,1)",
			"insert into memories(id,kind,content,privacy,status,supersedes,workspace_id,created_at,updated_at) values('m-new','fact','x','public','active','m-sup','w',1,1)",
			"insert into memories(id,kind,content,privacy,status,workspace_id,created_at,updated_at) values('m-fresh-del','fact','x','public','deleted','w',1,?)",
		} {
			args := []any{}
			if q[len(q)-2:] == "?)" {
				args = append(args, fresh)
			}
			if _, err := tx.Exec(q, args...); err != nil {
				return err
			}
		}
		_, err := tx.Exec("insert into shell_log(id,task_id,command,verdict,status,created_at,finished_at) values('sl-old',?,'ls','safe','ran',?,?),('sl-new',?,'ls','safe','ran',?,?),('sl-running',?,'ls','safe','running',?,NULL)",
			tid, old, old, tid, fresh, fresh, tid, old)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	auditBefore := count(h, "select count(*) from audit_events")

	setRetention(h, `{"read_notifications_days":30,"shell_log_days":30,"retired_memories_days":90,"consumed_approvals_days":90,"enforce":false}`)
	if n, err := h.eng.PurgeRetention(ctx); n != nil || err != nil {
		t.Fatalf("enforce=false is a report only: %v %v", n, err)
	}
	if count(h, "select count(*) from notifications") != 3 {
		t.Fatal("deleted while not enforced")
	}

	setRetention(h, `{"read_notifications_days":1,"enforce":true}`)
	if _, err := h.eng.PurgeRetention(ctx); !errors.Is(err, ErrInvalid) || count(h, "select count(*) from notifications") != 3 {
		t.Fatalf("a number under the floor must refuse the whole purge: %v", err)
	}

	setRetention(h, `{"read_notifications_days":30,"shell_log_days":30,"retired_memories_days":90,"consumed_approvals_days":90,"enforce":true}`)
	got, err := h.eng.PurgeRetention(ctx)
	if err != nil || got["read_notifications"] != 1 || got["shell_log"] != 1 || got["retired_memories"] != 1 || got["consumed_approvals"] != 0 {
		t.Fatalf("%v %v", got, err)
	}
	for q, want := range map[string]int{
		"select count(*) from notifications where id='n-old'":          0, // old and read: gone
		"select count(*) from notifications where id='n-unread-old'":   1, // never read: kept
		"select count(*) from notifications where id='n-new'":          1, // too recent
		"select count(*) from shell_log where id='sl-old'":             0,
		"select count(*) from shell_log where id='sl-new'":             1,
		"select count(*) from shell_log where id='sl-running'":         1, // unfinished: kept
		"select count(*) from memories where id='m-del'":               0, // retired and old and unreferenced: gone
		"select count(*) from memories where id='m-sup'":               1, // a newer memory still supersedes it: kept (and the foreign key would refuse)
		"select count(*) from memories where id in ('m-keep','m-new')": 2, // active: never
		"select count(*) from memories where id='m-fresh-del'":         1, // retired but recent
		"select count(*) from tasks where id=?":                        1,
	} {
		args := []any{}
		if want == 1 && len(q) > 0 && q[len(q)-2:] == "=?" {
			args = append(args, tid)
		}
		if count(h, q, args...) != want {
			t.Errorf("%s: want %d", q, want)
		}
	}
	if count(h, "select count(*) from audit_events where type='RETENTION_PURGED'") != 1 || count(h, "select count(*) from audit_events") != auditBefore+1 {
		t.Fatal("exactly one audit entry must record the purge, and no audit entry may be deleted")
	}
	if count(h, "select count(*) from audit_events where type='RETENTION_PURGED' and (data_json like '%old read%' or data_json like '%command%' or data_json like '%n-old%')") != 0 {
		t.Fatal("the audit entry names counts, not content")
	}
	if again, _ := h.eng.PurgeRetention(ctx); again["read_notifications"]+again["shell_log"]+again["retired_memories"] != 0 {
		t.Fatalf("a second purge finds nothing: %v", again)
	}
}
