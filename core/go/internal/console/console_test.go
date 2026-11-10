package console

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"aura.local/core/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

type fakeAPI struct {
	approved, rejected, answered string
	stopped, cancelled           bool
	raw                          []string // "METHOD path body" of Raw calls
}

var panelFixtures = map[string]string{
	"/memories?limit=100": `{"memories":[{"id":"01MEMAAAAAAAAAAAAAAAAAAAAA","kind":"fact","status":"active","privacy":"private","content":"The owner prefers\nshort reports","provenance":{"task_id":"T1"}}]}`,
	"/skills":             `{"skills":[{"id":"01SKLAAAAAAAAAAAAAAAAAAAAA","name":"fetch and summarize","status":"active","current_version":3,"notes":"two pages"}]}`,
	"/limitations":        `{"limitations":[{"id":"01LIMAAAAAAAAAAAAAAAAAAAAA","status":"open","category":"external_dependency","title":"free routes rate limited"}]}`,
	"/notifications":      `{"notifications":[{"id":"01NOTAAAAAAAAAAAAAAAAAAAAA","kind":"info","title":"A task is waiting for your answer","read_at":null,"created_at":1793000000000}]}`,
	"/self":               `{"identity":"AURA","open_limitations":1}`,
}

func (f *fakeAPI) Raw(method, path string, body any) (json.RawMessage, error) {
	b, _ := json.Marshal(body)
	f.raw = append(f.raw, method+" "+path+" "+string(b))
	if method == "GET" {
		return json.RawMessage(panelFixtures[path]), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakeAPI) Tasks() ([]client.Task, error) {
	return []client.Task{{ID: "01TASKAAAAAAAAAAAAAAAAAAAA", State: "WAITING_APPROVAL", Objective: strings.Repeat("very long objective ", 10)}}, nil
}
func (f *fakeAPI) Timeline(id string) (*client.Timeline, error) {
	return &client.Timeline{Task: client.Task{ID: id, State: "WAITING_APPROVAL", Objective: "publish the report"},
		Decisions: []map[string]any{{"kind": "approval", "tool_id": "workspace.write", "target": "published/r.md"}},
		Spend:     map[string]any{"tree_cost_usd": 0.0}}, nil
}
func (f *fakeAPI) Create(string, bool, []string) (*client.Task, error) {
	return &client.Task{ID: "NEW"}, nil
}
func (f *fakeAPI) Approvals() ([]client.Approval, error) {
	return []client.Approval{{ID: "AP1", TaskID: "01TASKAAAAAAAAAAAAAAAAAAAA", ToolID: "workspace.write", Target: "published/r.md",
		SideEffect: "write_idempotent", ActionHash: strings.Repeat("ab", 32), Parameters: map[string]any{"path": "published/r.md", "content": "line1\nline2"}}}, nil
}
func (f *fakeAPI) Approve(id, hash string) error { f.approved = id + ":" + hash; return nil }
func (f *fakeAPI) Reject(id string) error        { f.rejected = id; return nil }
func (f *fakeAPI) Answer(_, a string) error      { f.answered = a; return nil }
func (f *fakeAPI) Correct(string, string) error  { return nil }
func (f *fakeAPI) Cancel(string) error           { f.cancelled = true; return nil }
func (f *fakeAPI) Stop() error                   { f.stopped = true; return nil }
func (f *fakeAPI) Files(string) ([]client.File, error) {
	return []client.File{{Path: "published/r.md", Bytes: 11}}, nil
}
func (f *fakeAPI) File(string, string) (string, error) { return "line1\nline2", nil }
func (f *fakeAPI) Stream(ctx context.Context, _ string, out chan<- client.Event) error {
	out <- client.Event{Seq: 1, Type: "TASK_CREATED", Actor: "owner", Data: []byte(`{"budget":1}`)}
	<-ctx.Done()
	return nil
}

// run executes a command and feeds resulting messages back (one level of batch).
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		for _, c := range b {
			if c != nil {
				if inner := c(); inner != nil {
					if _, isTick := inner.(tickMsg); !isTick {
						m.Update(inner)
					}
				}
			}
		}
		return
	}
	if msg != nil {
		m.Update(msg)
	}
}

func key(m *Model, s string) {
	var k tea.KeyMsg
	switch s {
	case "enter":
		k = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		k = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		k = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	_, cmd := m.Update(k)
	run(m, cmd)
}

func fits(t *testing.T, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Fatalf("view has %d lines > %d", len(lines), h)
	}
	for _, l := range lines {
		if n := utf8.RuneCountInString(stripANSI(l)); n > w {
			t.Fatalf("line wider than %d (%d): %q", w, n, stripANSI(l))
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestApproveWithFullPreviewIn80x24(t *testing.T) {
	api := &fakeAPI{}
	m := New(api)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	run(m, m.loadTasks())
	fits(t, m.View(), 80, 24)
	key(m, "enter") // open detail
	if m.mode != modeDetail || !strings.Contains(m.View(), "APPROVAL needed") {
		t.Fatalf("detail: %s", m.View())
	}
	fits(t, m.View(), 80, 24)
	key(m, "y") // preview
	v := m.View()
	if m.mode != modeApproval || !strings.Contains(v, strings.Repeat("ab", 8)) || !strings.Contains(v, "line2") {
		t.Fatalf("preview lacks hash or parameters:\n%s", v)
	}
	fits(t, v, 80, 24)
	if api.approved != "" {
		t.Fatal("approved before confirmation")
	}
	key(m, "y")
	if api.approved != "AP1:"+strings.Repeat("ab", 32) {
		t.Fatalf("approve call %q", api.approved)
	}
	m.stopStream()
}

func TestKillSwitchNeedsConfirmation(t *testing.T) {
	api := &fakeAPI{}
	m := New(api)
	key(m, "!")
	if m.mode != modeConfirm || api.stopped {
		t.Fatal("kill switch without confirmation")
	}
	key(m, "n")
	if api.stopped {
		t.Fatal("stopped after declining")
	}
	key(m, "!")
	key(m, "y")
	if !api.stopped || !strings.Contains(m.status, "KILL SWITCH") {
		t.Fatalf("stopped=%v status=%q", api.stopped, m.status)
	}
}

func TestAnswerQuestion(t *testing.T) {
	api := &fakeAPI{}
	m := New(api)
	run(m, m.loadTasks())
	key(m, "enter")
	key(m, "a")
	for _, r := range "use page two" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	key(m, "enter")
	if api.answered != "use page two" {
		t.Fatalf("answer %q", api.answered)
	}
	m.stopStream()
}

func lastRaw(api *fakeAPI) string {
	if len(api.raw) == 0 {
		return ""
	}
	return api.raw[len(api.raw)-1]
}

// Console v2 (Blueprint §13A.3): every panel renders in 80×24 and spells out state.
func TestConsoleV2PanelsFit80x24(t *testing.T) {
	for _, k := range []string{"m", "s", "i", "l", "o"} {
		api := &fakeAPI{}
		m := New(api)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		key(m, k)
		v := m.View()
		if m.mode != modePanel || len(m.items) != 1 {
			t.Fatalf("panel %s: mode %d items %d\n%s", k, m.mode, len(m.items), v)
		}
		fits(t, v, 80, 24)
		key(m, "enter")
		if m.mode != modePanelItem {
			t.Fatalf("panel %s: item not opened", k)
		}
		fits(t, m.View(), 80, 24)
		key(m, "esc")
		key(m, "esc")
		if m.mode != modeList {
			t.Fatalf("panel %s: esc does not return to the task list", k)
		}
	}
}

func TestMemoryDeleteNeedsConfirmation(t *testing.T) {
	api := &fakeAPI{}
	m := New(api)
	key(m, "m")
	key(m, "d")
	if m.mode != modeConfirm || strings.HasPrefix(lastRaw(api), "DELETE") {
		t.Fatal("deleted without confirmation")
	}
	key(m, "n")
	if strings.HasPrefix(lastRaw(api), "DELETE") || m.mode != modePanel {
		t.Fatal("deleted after declining")
	}
	key(m, "d")
	key(m, "y")
	if !strings.Contains(strings.Join(api.raw, "|"), "DELETE /memories/01MEMAAAAAAAAAAAAAAAAAAAAA") || m.status != "memory deleted" {
		t.Fatalf("calls %v status %q", api.raw, m.status)
	}
}

func TestSkillActionsAndRollback(t *testing.T) {
	api := &fakeAPI{}
	m := New(api)
	key(m, "s")
	key(m, "x")
	if !strings.Contains(strings.Join(api.raw, "|"), "POST /skills/01SKLAAAAAAAAAAAAAAAAAAAAA/disable") {
		t.Fatalf("disable not sent: %v", api.raw)
	}
	key(m, "b")
	key(m, "y")
	if !strings.Contains(strings.Join(api.raw, "|"), `POST /skills/01SKLAAAAAAAAAAAAAAAAAAAAA/rollback {"version":2}`) {
		t.Fatalf("rollback to the previous version not sent: %v", api.raw)
	}
}

func TestOpeningNotificationMarksItRead(t *testing.T) {
	api := &fakeAPI{}
	m := New(api)
	key(m, "o")
	if !strings.Contains(m.View(), "unread") {
		t.Fatal("read state not spelled out")
	}
	key(m, "enter")
	if !strings.Contains(strings.Join(api.raw, "|"), "POST /notifications/01NOTAAAAAAAAAAAAAAAAAAAAA/read") {
		t.Fatalf("not marked read: %v", api.raw)
	}
}
