package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/channel"
	"aura.local/core/internal/engine"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/task"
)

type fakeTelegram struct {
	mu      sync.Mutex
	updates []map[string]any
	sent    []map[string]any
	token   string
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.URL.Path, "/bot"+f.token+"/") {
		http.Error(w, `{"ok":false,"description":"Unauthorized"}`, 401)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	switch strings.TrimPrefix(r.URL.Path, "/bot"+f.token+"/") {
	case "getUpdates":
		off := int64(body["offset"].(float64))
		var out []map[string]any
		for _, u := range f.updates {
			if int64(u["update_id"].(int)) >= off {
				out = append(out, u)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": out})
	case "sendMessage":
		f.sent = append(f.sent, body)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	}
}

func msg(id int, from int64, text string, replyTo string) map[string]any {
	m := map[string]any{"message_id": id, "text": text, "from": map[string]any{"id": from}, "chat": map[string]any{"id": from, "type": "private"}}
	if replyTo != "" {
		m["reply_to_message"] = map[string]any{"text": replyTo}
	}
	return map[string]any{"update_id": id, "message": m}
}

func (f *fakeTelegram) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m["text"].(string))
	}
	return out
}

// D.1 P5b messaging channel: only the owner is served; tasks can be created
// and questions answered; approvals are never given in chat; each
// notification is delivered once (OL-11).
func TestTelegramChannel(t *testing.T) {
	e := newEnv(t, 1000)
	ft := &fakeTelegram{token: "123:fake-token"}
	srv := httptest.NewServer(ft)
	defer srv.Close()
	const owner, stranger = 1001, 666
	bot := channel.NewTelegram(channel.TelegramConfig{Token: ft.token, BaseURL: srv.URL, AllowedUserIDs: []int64{owner},
		DefaultTools: []string{"web.fetch", "workspace.write"}, DefaultDomains: []string{"example.com"}, WebOrigin: "https://aura.test.example",
		PollTimeout: time.Second}, e.a.Engine, e.a.Store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	ft.updates = []map[string]any{
		msg(1, stranger, "Fetch https://example.com/ and write reports/x.md", ""),
		msg(2, owner, "Fetch https://example.com/ and write reports/a.md", ""),
		msg(3, owner, "/approve 01APPROVALAAAAAAAAAAAAAAAA", ""),
	}
	if err := bot.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	ts, _ := task.List(ctx, e.a.Store.R, "", 10, "")
	if len(ts) != 1 || !strings.Contains(ts[0].Objective, "reports/a.md") {
		t.Fatalf("tasks: %d (the stranger's message must not create one)", len(ts))
	}
	out := strings.Join(ft.texts(), "\n---\n")
	if !strings.Contains(out, "Task created: "+ts[0].ID) || !strings.Contains(out, "Approvals cannot be given in chat") {
		t.Fatalf("replies: %s", out)
	}
	var refused int
	e.a.Store.R.QueryRow("select count(*) from audit_events where type='CHANNEL_REFUSED'").Scan(&refused)
	if refused != 1 {
		t.Fatalf("stranger not audited: %d", refused)
	}
	// Updates are not handled twice.
	n := len(ft.texts())
	bot.PollOnce(ctx)
	if len(ft.texts()) != n {
		t.Fatal("an update was handled twice")
	}
	// A question is relayed once; a reply to it answers the task.
	tk := ts[0]
	e.a.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "insert into task_inputs(id, task_id, question, status, created_at) values(?,?,?,?,?)",
			ids.New(), tk.ID, "Which page?", "pending", ids.NowMs()); err != nil {
			return err
		}
		if _, err := task.Transition(ctx, tx, tk.ID, task.Planning, "", "", "core"); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, tk.ID, nil, "INPUT_REQUESTED", "worker", map[string]any{"question": "Which page?"}); err != nil {
			return err
		}
		_, err := task.Transition(ctx, tx, tk.ID, task.WaitingInput, "", "input:x", "core")
		return err
	})
	bot.NotifyOnce(ctx)
	bot.NotifyOnce(ctx)
	asks := 0
	var question string
	for _, s := range ft.texts() {
		if strings.Contains(s, "asks: Which page?") {
			asks++
			question = s
		}
	}
	if asks != 1 {
		t.Fatalf("question relayed %d times", asks)
	}
	ft.mu.Lock()
	ft.updates = append(ft.updates, msg(4, owner, "use https://example.com/", question))
	ft.mu.Unlock()
	bot.PollOnce(ctx)
	cur, _ := task.Get(ctx, e.a.Store.R, tk.ID)
	if cur.State == task.WaitingInput {
		t.Fatalf("the reply did not answer the question: %s", strings.Join(ft.texts(), " | "))
	}
	_ = engine.ErrInvalid
}
