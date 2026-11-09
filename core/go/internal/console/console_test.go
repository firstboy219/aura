package console

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"aura.local/core/internal/client"
	tea "github.com/charmbracelet/bubbletea"
)

type fakeAPI struct {
	approved, rejected, answered string
	stopped, cancelled           bool
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
