// Package channel is AURA's first messaging channel (Blueprint D.1 P5b,
// 13A.7, ADR-0044): a Telegram bot over the official Bot API, long-polled from
// the Core (outbound only). Only the owner's Telegram user ids are served.
//
// The channel is a client of the same engine operations as the API: it can
// create tasks, show status, answer a task's question, cancel, and it relays
// owner notifications (completed, failed, question, approval needed). It can
// never approve: approvals happen in the Web App with step-up (13A.7 rule 4:
// chat may request an approval but cannot sign one). Each notification is
// delivered once (OL-11). Messages are plain text (no markup parsing).
package channel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aura.local/core/internal/engine"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
)

// TelegramConfig configures the channel.
type TelegramConfig struct {
	Token          string // bot token (from the vault)
	BaseURL        string // https://api.telegram.org (tests: a fake server)
	AllowedUserIDs []int64
	DefaultTools   []string
	DefaultDomains []string
	WebOrigin      string // links to the Web App for approvals
	PollTimeout    time.Duration
}

// Telegram runs the bot.
type Telegram struct {
	cfg TelegramConfig
	eng *engine.Engine
	st  *store.Store
	log *slog.Logger
	hc  *http.Client
}

const settingOffset = "telegram_offset"

func NewTelegram(cfg TelegramConfig, eng *engine.Engine, st *store.Store, log *slog.Logger) *Telegram {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.telegram.org"
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 30 * time.Second
	}
	return &Telegram{cfg: cfg, eng: eng, st: st, log: log, hc: &http.Client{Timeout: cfg.PollTimeout + 15*time.Second}}
}

func (t *Telegram) api(ctx context.Context, method string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.BaseURL+"/bot"+t.cfg.Token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return errors.New("telegram: request failed") // never echo the URL: it contains the token
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if json.Unmarshal(raw, &env) != nil || !env.OK {
		return fmt.Errorf("telegram: %s failed: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (t *Telegram) allowed(id int64) bool {
	for _, a := range t.cfg.AllowedUserIDs {
		if a == id {
			return true
		}
	}
	return false
}

// Send sends plain text to a chat.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	if len(text) > 3900 {
		text = text[:3900] + "…"
	}
	return t.api(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}, nil)
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		From      struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		ReplyTo *struct {
			Text string `json:"text"`
		} `json:"reply_to_message"`
	} `json:"message"`
}

func (t *Telegram) offset(ctx context.Context) int64 {
	var v string
	t.st.R.QueryRowContext(ctx, "select value from settings where key=?", settingOffset).Scan(&v)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (t *Telegram) setOffset(ctx context.Context, n int64) {
	t.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?,?,?)
			on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, settingOffset, strconv.FormatInt(n, 10), ids.NowMs())
		return err
	})
}

// PollOnce fetches and handles one batch of updates (tests call it directly).
func (t *Telegram) PollOnce(ctx context.Context) error {
	var ups []update
	if err := t.api(ctx, "getUpdates", map[string]any{"offset": t.offset(ctx), "timeout": int(t.cfg.PollTimeout.Seconds()),
		"allowed_updates": []string{"message"}}, &ups); err != nil {
		return err
	}
	for _, u := range ups {
		t.setOffset(ctx, u.UpdateID+1) // at most once: an update is never handled twice
		if u.Message == nil {
			continue
		}
		m := u.Message
		if !t.allowed(m.From.ID) || m.Chat.Type != "private" {
			t.log.Warn("telegram: message from a user who is not allowed (add the id to channels.telegram.allowed_user_ids to allow)",
				"user_id", m.From.ID, "chat_type", m.Chat.Type)
			t.st.Write(ctx, func(tx *sql.Tx) error {
				return policy.Audit(ctx, tx, "CHANNEL_REFUSED", "telegram", "", "", map[string]any{"user_id": m.From.ID})
			})
			continue
		}
		reply := t.handle(ctx, m.Text, m.ReplyTo)
		if reply != "" {
			if err := t.Send(ctx, m.Chat.ID, reply); err != nil {
				t.log.Warn("telegram send", "err", err)
			}
		}
	}
	return nil
}

var taskRef = func(s string) string {
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "[]():,.")
		if len(f) == 26 && strings.HasPrefix(f, "01") {
			return f
		}
	}
	return ""
}

func (t *Telegram) handle(ctx context.Context, text string, replyTo *struct {
	Text string `json:"text"`
}) string {
	text = strings.TrimSpace(text)
	cmd, rest, _ := strings.Cut(text, " ")
	rest = strings.TrimSpace(rest)
	audit := func(action string, data map[string]any) {
		data["action"] = action
		t.st.Write(ctx, func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, "CHANNEL_COMMAND", "owner(telegram)", "", "", data)
		})
	}
	// A reply to AURA's question is the answer.
	if replyTo != nil && strings.Contains(replyTo.Text, "asks:") && !strings.HasPrefix(text, "/") {
		id := taskRef(replyTo.Text)
		if id == "" {
			return "I could not tell which task this answers; use /answer <task-id> <text>."
		}
		if _, err := t.eng.Answer(ctx, id, text); err != nil {
			return "Answer not accepted: " + err.Error()
		}
		audit("answer", map[string]any{"task_id": id})
		return "Answer sent to " + id + "."
	}
	switch cmd {
	case "/start", "/help":
		return "AURA. Send a message to create a task.\n/tasks — recent tasks\n/status <id>\n/answer <id> <text>\n/cancel <id>\n" +
			"Approvals are given in the Web App: " + t.cfg.WebOrigin + "/app/#/approvals"
	case "/tasks":
		ts, err := task.List(ctx, t.st.R, "", 5, "")
		if err != nil {
			return "Could not list tasks."
		}
		var sb strings.Builder
		for _, tk := range ts {
			fmt.Fprintf(&sb, "%s %s — %s\n", tk.ID, tk.State, trunc(tk.Objective, 80))
		}
		if sb.Len() == 0 {
			return "No tasks yet."
		}
		return sb.String()
	case "/status":
		tk, err := task.Get(ctx, t.st.R, taskRef(rest))
		if err != nil {
			return "No such task."
		}
		s := fmt.Sprintf("%s\n%s\nState: %s\nCost: $%.4f", tk.ID, trunc(tk.Objective, 300), tk.State, tk.CostUSD+tk.CostReservedUSD)
		if tk.Reason != nil {
			s += "\nReason: " + *tk.Reason
		}
		return s + "\n" + t.cfg.WebOrigin + "/app/#/task/" + tk.ID
	case "/answer":
		id := taskRef(rest)
		ans := strings.TrimSpace(strings.TrimPrefix(rest, id))
		if id == "" || ans == "" {
			return "Use /answer <task-id> <text>."
		}
		if _, err := t.eng.Answer(ctx, id, ans); err != nil {
			return "Answer not accepted: " + err.Error()
		}
		audit("answer", map[string]any{"task_id": id})
		return "Answer sent."
	case "/cancel":
		id := taskRef(rest)
		if _, err := t.eng.Cancel(ctx, id); err != nil {
			return "Not cancelled: " + err.Error()
		}
		audit("cancel", map[string]any{"task_id": id})
		return "Cancelled " + id + "."
	case "/approve", "/reject":
		return "Approvals cannot be given in chat. Open " + t.cfg.WebOrigin + "/app/#/approvals (it asks for your authenticator code)."
	}
	if strings.HasPrefix(cmd, "/") {
		return "Unknown command. /help"
	}
	tk, _, err := t.eng.Submit(ctx, engine.CreateInput{Objective: text, Tools: t.cfg.DefaultTools, AllowDomains: t.cfg.DefaultDomains})
	if err != nil {
		return "Task not created: " + err.Error()
	}
	audit("create_task", map[string]any{"task_id": tk.ID})
	return "Task created: " + tk.ID + "\nI will tell you when it needs you or is done.\n" + t.cfg.WebOrigin + "/app/#/task/" + tk.ID
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// notification is one owner-facing event to relay.
type notification struct {
	item, text string
}

// pending collects events the owner should hear about, not yet delivered.
func (t *Telegram) pending(ctx context.Context) []notification {
	var out []notification
	rows, err := t.st.R.QueryContext(ctx, `select e.id, e.task_id, e.type, e.data_json, t.objective, coalesce(t.reason,'') from task_events e
		join tasks t on t.id = e.task_id
		where e.type in ('INPUT_REQUESTED','APPROVAL_REQUESTED','STATE_CHANGED') and t.agent_role = 'main' and e.at > ?
		and not exists (select 1 from channel_deliveries d where d.channel='telegram' and d.item=e.id)
		order by e.id limit 50`, time.Now().Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id, taskID, typ, data, obj, reason string
		rows.Scan(&id, &taskID, &typ, &data, &obj, &reason)
		var d map[string]any
		json.Unmarshal([]byte(data), &d)
		link := t.cfg.WebOrigin + "/app/#/task/" + taskID
		switch typ {
		case "INPUT_REQUESTED":
			out = append(out, notification{id, fmt.Sprintf("AURA [%s] asks: %v\n(reply to this message to answer)\n%s", taskID, d["question"], link)})
		case "APPROVAL_REQUESTED":
			out = append(out, notification{id, fmt.Sprintf("Approval needed [%s]: %v on %v\nApprove or reject in the Web App (needs your authenticator code):\n%s/app/#/approvals",
				taskID, d["tool_id"], d["target"], t.cfg.WebOrigin)})
		case "STATE_CHANGED":
			to, _ := d["to"].(string)
			if to == "COMPLETED" || to == "FAILED" || to == "BUDGET_EXCEEDED" {
				msg := fmt.Sprintf("Task %s %s: %s\n%s", taskID, to, trunc(obj, 200), link)
				if to != "COMPLETED" && reason != "" {
					msg += "\nReason: " + reason
				}
				out = append(out, notification{id, msg})
			} else {
				out = append(out, notification{id, ""}) // not relayed, but marked as seen
			}
		}
	}
	return out
}

// NotifyOnce relays pending notifications to every allowed user, once each.
func (t *Telegram) NotifyOnce(ctx context.Context) {
	for _, n := range t.pending(ctx) {
		if n.text != "" {
			for _, uid := range t.cfg.AllowedUserIDs { // a private chat's id is the user's id
				if err := t.Send(ctx, uid, n.text); err != nil {
					t.log.Warn("telegram notify", "err", err)
					return // retry next round; nothing was marked delivered
				}
			}
		}
		t.st.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "insert or ignore into channel_deliveries(channel, item, delivered_at) values('telegram',?,?)", n.item, ids.NowMs())
			return err
		})
	}
}

// Run polls and notifies until ctx ends.
func (t *Telegram) Run(ctx context.Context) {
	t.log.Info("telegram channel running", "allowed_users", len(t.cfg.AllowedUserIDs))
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				t.NotifyOnce(ctx)
			}
		}
	}()
	for ctx.Err() == nil {
		if err := t.PollOnce(ctx); err != nil && ctx.Err() == nil {
			t.log.Warn("telegram poll", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
			}
		}
	}
}

var _ = url.PathEscape
