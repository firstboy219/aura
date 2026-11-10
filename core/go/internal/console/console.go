// Package console is the AURA Console v1 (Blueprint P2, ADR-0022): a terminal
// UI over the public API and event stream. It works in 80×24 over SSH, and
// every action has a CLI equivalent (shown in the help line).
package console

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"aura.local/core/internal/client"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type mode int

const (
	modeList mode = iota
	modeDetail
	modeInput
	modeConfirm
	modeApproval
	modeFiles
	modeFile
	modePanel     // Console v2 list: memory, skills, self-model, ledger, notifications
	modePanelItem // one item of a panel, in full
)

// Model is the console state.
type Model struct {
	api    client.API
	width  int
	height int
	mode   mode
	prev   mode

	tasks    []client.Task
	cursor   int
	selected string
	timeline *client.Timeline
	events   []client.Event
	approval *client.Approval
	files    []client.File
	fileText string
	status   string

	input      textinput.Model
	inputFor   string // new | answer | correct
	confirmFor string // stop | cancel
	public     bool

	streamCancel context.CancelFunc
	streamCh     chan client.Event

	panel     string // memory | skills | self | ledger | notifications
	items     []panelItem
	itemText  string
	confirmID string // panel item a confirmation is about
}

// New creates the console model.
func New(api client.API) *Model {
	ti := textinput.New()
	ti.CharLimit = 4000
	return &Model{api: api, width: 80, height: 24, input: ti}
}

type tickMsg time.Time
type tasksMsg []client.Task
type timelineMsg *client.Timeline
type eventMsg client.Event
type errMsg struct{ err error }
type statusMsg string
type filesMsg []client.File
type fileMsg string

func tick() tea.Cmd { return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m *Model) Init() tea.Cmd { return tea.Batch(m.loadTasks(), tick()) }

func (m *Model) loadTasks() tea.Cmd {
	return func() tea.Msg {
		ts, err := m.api.Tasks()
		if err != nil {
			return errMsg{err}
		}
		return tasksMsg(ts)
	}
}

func (m *Model) loadTimeline() tea.Cmd {
	id := m.selected
	return func() tea.Msg {
		tl, err := m.api.Timeline(id)
		if err != nil {
			return errMsg{err}
		}
		return timelineMsg(tl)
	}
}

func (m *Model) waitEvent() tea.Cmd {
	ch := m.streamCh
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func (m *Model) openDetail(id string) tea.Cmd {
	m.stopStream()
	m.selected, m.mode, m.events, m.timeline = id, modeDetail, nil, nil
	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	ch := make(chan client.Event, 64)
	m.streamCh = ch
	go func() {
		defer close(ch)
		m.api.Stream(ctx, id, ch)
	}()
	return tea.Batch(m.loadTimeline(), m.waitEvent())
}

func (m *Model) stopStream() {
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
}

func (m *Model) do(f func() error, ok string) tea.Cmd {
	return func() tea.Msg {
		if err := f(); err != nil {
			return errMsg{err}
		}
		return statusMsg(ok)
	}
}

// Update handles messages and keys.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		cmds := []tea.Cmd{tick(), m.loadTasks()}
		if m.selected != "" && m.mode != modeList {
			cmds = append(cmds, m.loadTimeline())
		}
		return m, tea.Batch(cmds...)
	case tasksMsg:
		m.tasks = msg
		if m.cursor >= len(m.tasks) {
			m.cursor = max(0, len(m.tasks)-1)
		}
		return m, nil
	case timelineMsg:
		m.timeline = msg
		return m, nil
	case eventMsg:
		m.events = append(m.events, client.Event(msg))
		if len(m.events) > 500 {
			m.events = m.events[len(m.events)-500:]
		}
		return m, m.waitEvent()
	case errMsg:
		m.status = "error: " + msg.err.Error()
		return m, nil
	case statusMsg:
		m.status = string(msg)
		return m, tea.Batch(m.loadTasks(), m.loadTimeline())
	case filesMsg:
		m.files, m.mode, m.cursor = msg, modeFiles, 0
		return m, nil
	case fileMsg:
		m.fileText, m.mode = string(msg), modeFile
		return m, nil
	case approvalMsg:
		m.handleApproval(msg)
		return m, nil
	case panelMsg:
		if msg.panel == m.panel {
			m.items = msg.items
			if m.cursor >= len(m.items) {
				m.cursor = max(0, len(m.items)-1)
			}
		}
		return m, nil
	case panelStatusMsg:
		m.status = string(msg)
		return m, m.loadPanel()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		m.stopStream()
		return m, tea.Quit
	}
	switch m.mode {
	case modeInput:
		switch k.String() {
		case "esc":
			m.mode = m.prev
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			m.mode = m.prev
			if text == "" {
				return m, nil
			}
			id := m.selected
			switch m.inputFor {
			case "new":
				public := m.public
				return m, func() tea.Msg {
					t, err := m.api.Create(text, public, nil)
					if err != nil {
						return errMsg{err}
					}
					return statusMsg("created " + t.ID)
				}
			case "answer":
				return m, m.do(func() error { return m.api.Answer(id, text) }, "answer sent")
			case "correct":
				return m, m.do(func() error { return m.api.Correct(id, text) }, "correction noted for the whole task tree")
			}
			return m, nil
		case "tab":
			if m.inputFor == "new" {
				m.public = !m.public
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return m, cmd
	case modeConfirm:
		m.mode = m.prev
		if k.String() != "y" {
			m.status = "cancelled"
			return m, nil
		}
		switch m.confirmFor {
		case "stop":
			return m, m.do(m.api.Stop, "KILL SWITCH ACTIVE: all activity stopped until the Core restarts")
		case "cancel":
			id := m.selected
			return m, m.do(func() error { return m.api.Cancel(id) }, "task cancelled")
		default:
			return m, m.confirmPanel()
		}
	case modeApproval:
		ap := m.approval
		switch k.String() {
		case "y":
			m.mode = modeDetail
			return m, m.do(func() error { return m.api.Approve(ap.ID, ap.ActionHash) }, "approved "+short(ap.ID))
		case "r":
			m.mode = modeDetail
			return m, m.do(func() error { return m.api.Reject(ap.ID) }, "rejected "+short(ap.ID))
		case "esc", "n", "q":
			m.mode = modeDetail
		}
		return m, nil
	case modeFiles:
		switch k.String() {
		case "esc", "q":
			m.mode = modeDetail
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.files)-1, m.cursor+1)
		case "enter":
			if len(m.files) > 0 {
				id, p := m.selected, m.files[m.cursor].Path
				return m, func() tea.Msg {
					s, err := m.api.File(id, p)
					if err != nil {
						return errMsg{err}
					}
					return fileMsg(s)
				}
			}
		}
		return m, nil
	case modeFile:
		if k.String() == "esc" || k.String() == "q" {
			m.mode = modeFiles
		}
		return m, nil
	case modePanel, modePanelItem:
		return m.panelKey(k)
	}
	// list and detail share global keys
	switch k.String() {
	case "q":
		if m.mode == modeDetail {
			m.stopStream()
			m.mode, m.selected = modeList, ""
			return m, nil
		}
		m.stopStream()
		return m, tea.Quit
	case "!":
		m.prev, m.mode, m.confirmFor = m.mode, modeConfirm, "stop"
		return m, nil
	case "n":
		m.startInput("new", "Objective (Tab toggles public/private): ")
		return m, textinput.Blink
	}
	if m.mode == modeList {
		switch k.String() {
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.tasks)-1, m.cursor+1)
		case "enter":
			if len(m.tasks) > 0 {
				return m, m.openDetail(m.tasks[m.cursor].ID)
			}
		case "r":
			return m, m.loadTasks()
		default:
			if p, ok := panelKeys[k.String()]; ok {
				return m, m.openPanel(p)
			}
		}
		return m, nil
	}
	// detail
	switch k.String() {
	case "esc":
		m.stopStream()
		m.mode, m.selected = modeList, ""
	case "y":
		id := m.selected
		return m, func() tea.Msg {
			aps, err := m.api.Approvals()
			if err != nil {
				return errMsg{err}
			}
			for _, ap := range aps {
				if ap.TaskID == id {
					a := ap
					return approvalMsg{&a}
				}
			}
			return statusMsg("no pending approval for this task")
		}
	case "a":
		m.startInput("answer", "Answer: ")
		return m, textinput.Blink
	case "c":
		m.startInput("correct", "Correction for the task tree: ")
		return m, textinput.Blink
	case "x":
		m.prev, m.mode, m.confirmFor = m.mode, modeConfirm, "cancel"
	case "f":
		id := m.selected
		return m, func() tea.Msg {
			fl, err := m.api.Files(id)
			if err != nil {
				return errMsg{err}
			}
			return filesMsg(fl)
		}
	}
	return m, nil
}

type approvalMsg struct{ ap *client.Approval }

func (m *Model) startInput(purpose, prompt string) {
	m.prev, m.mode, m.inputFor = m.mode, modeInput, purpose
	m.input.Reset()
	m.input.Prompt = prompt
	m.input.Focus()
}

// approvalMsg is handled here to keep Update's switch on data messages small.
func (m *Model) handleApproval(msg approvalMsg) {
	m.approval, m.mode = msg.ap, modeApproval
}

var (
	bold   = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Faint(true)
	warn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	header = lipgloss.NewStyle().Bold(true).Reverse(true)
)

func short(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func clip(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if w <= 1 || len(r) <= w {
		return s
	}
	return string(r[:w-1]) + "…"
}

// View renders the current screen, never wider or taller than the terminal.
func (m *Model) View() string {
	w, h := max(40, m.width), max(10, m.height)
	var lines []string
	switch m.mode {
	case modeApproval:
		lines = m.viewApproval(w)
	case modeFiles:
		lines = append(lines, header.Render(clip(" Files of "+short(m.selected)+" ", w)))
		for i, f := range m.files {
			mark := "  "
			if i == m.cursor {
				mark = "> "
			}
			lines = append(lines, clip(fmt.Sprintf("%s%-50s %8d B", mark, f.Path, f.Bytes), w))
		}
		lines = append(lines, dim.Render(clip("enter view · esc back   (CLI: curl /api/v1/tasks/<id>/files)", w)))
	case modeFile:
		lines = append(lines, header.Render(clip(" File ", w)))
		for _, l := range strings.Split(m.fileText, "\n") {
			lines = append(lines, clip(l, w))
		}
	case modePanel:
		lines = m.viewPanel(w)
	case modePanelItem:
		lines = append(lines, header.Render(clip(" "+panelTitles[m.panel]+" ", w)))
		for _, l := range strings.Split(m.itemText, "\n") {
			lines = append(lines, clip(l, w))
		}
	default:
		if m.mode == modeList || (m.mode != modeDetail && m.selected == "") {
			lines = m.viewList(w, h)
		} else {
			lines = m.viewDetail(w, h)
		}
	}
	footer := m.footer(w)
	maxBody := h - len(footer)
	if len(lines) > maxBody {
		if m.mode == modeDetail || m.mode == modeFile {
			lines = append(lines[:2], lines[len(lines)-(maxBody-2):]...) // keep header, show newest
		} else {
			lines = lines[:maxBody]
		}
	}
	for len(lines) < maxBody {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, footer...), "\n")
}

func (m *Model) footer(w int) []string {
	var help string
	switch m.mode {
	case modeList:
		help = "↑↓ enter open · n new · m memory · s skills · i self · l ledger · o notices · ! kill · q quit"
	case modePanel, modePanelItem:
		help = panelHelp[m.panel]
	case modeDetail:
		help = "y approve · a answer · c correct · f files · x cancel · ! stop all · esc back"
	case modeInput:
		return []string{clip(m.input.View(), w), dim.Render(clip("enter send · esc cancel"+map[bool]string{true: " · Tab: public (free models allowed)", false: " · Tab: private"}[m.public], w))}
	case modeConfirm:
		q := "Cancel this task and all its subagents? [y/N]"
		switch m.confirmFor {
		case "stop":
			q = "KILL SWITCH: stop ALL AURA activity until the Core restarts? [y/N]   (CLI: aura stop --all)"
		case "delete-memory":
			q = "Delete this memory permanently? [y/N]   (CLI: aura memory delete <id>)"
		case "rollback-skill":
			q = "Roll this skill back to its previous version? [y/N]   (CLI: aura skill rollback <id> <version>)"
		}
		return []string{warn.Render(clip(q, w)), ""}
	default:
		help = "esc back"
	}
	return []string{clip(m.status, w), dim.Render(clip(help, w))}
}

func (m *Model) viewList(w, h int) []string {
	lines := []string{header.Render(clip(fmt.Sprintf(" AURA Console · %d tasks ", len(m.tasks)), w))}
	for i, t := range m.tasks {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		lines = append(lines, clip(fmt.Sprintf("%s%s %-16s $%.4f %s", mark, short(t.ID), t.State, t.CostUSD, t.Objective), w))
	}
	return lines
}

func (m *Model) viewDetail(w, h int) []string {
	tl := m.timeline
	if tl == nil {
		return []string{header.Render(clip(" Task "+short(m.selected)+" ", w)), "loading…"}
	}
	t := tl.Task
	lines := []string{
		header.Render(clip(fmt.Sprintf(" %s · %s · $%.4f ", short(t.ID), t.State, num(tl.Spend["tree_cost_usd"])), w)),
		clip(t.Objective, w),
	}
	if t.WaitReason != nil && *t.WaitReason != "" {
		lines = append(lines, bold.Render(clip("waiting: "+*t.WaitReason, w)))
	}
	if tl.NextStep != nil {
		lines = append(lines, clip(fmt.Sprintf("next: step %v %v — %v", tl.NextStep["step_no"], strOf(tl.NextStep["tool_id"]), tl.NextStep["description"]), w))
	}
	for _, d := range tl.Decisions {
		if d["kind"] == "question" {
			lines = append(lines, warn.Render(clip("QUESTION: "+strOf(d["question"])+"  (press a)", w)))
		} else {
			lines = append(lines, warn.Render(clip(fmt.Sprintf("APPROVAL needed: %s → %s  (press y)", strOf(d["tool_id"]), strOf(d["target"])), w)))
		}
	}
	if n := len(tl.Blockers); n > 0 {
		lines = append(lines, clip(fmt.Sprintf("blockers: %d (latest: %s)", n, blockerText(tl.Blockers[n-1])), w))
	}
	lines = append(lines, dim.Render(clip(fmt.Sprintf("done %d steps · live trace:", len(tl.Completed)), w)))
	for _, ev := range m.events {
		lines = append(lines, clip(fmt.Sprintf("%3d %-6s %-18s %s", ev.Seq, ev.Actor, ev.Type, compact(ev.Data)), w))
	}
	return lines
}

func (m *Model) viewApproval(w int) []string {
	ap := m.approval
	lines := []string{warn.Render(clip(" Approve exactly this action? ", w)),
		clip("approval: "+ap.ID, w), clip("task:     "+ap.TaskID, w), clip("tool:     "+ap.ToolID, w),
		clip("effect:   "+ap.SideEffect, w), clip("target:   "+ap.Target, w), clip("expires:  "+ap.ExpiresAt, w),
		clip("hash:     "+ap.ActionHash, w), "parameters:"}
	keys := make([]string, 0, len(ap.Parameters))
	for k := range ap.Parameters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := fmt.Sprint(ap.Parameters[k])
		for i, l := range strings.Split(v, "\n") {
			if i == 0 {
				lines = append(lines, clip("  "+k+": "+l, w))
			} else {
				lines = append(lines, clip("    "+l, w))
			}
		}
	}
	lines = append(lines, bold.Render(clip("y approve · r reject · esc decide later   (CLI: aura approve "+ap.ID+")", w)))
	return lines
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func strOf(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func blockerText(b map[string]any) string {
	if r, ok := b["reason"]; ok {
		return fmt.Sprint(r)
	}
	if d, ok := b["data"].(map[string]any); ok {
		if r, ok := d["reason"]; ok {
			return fmt.Sprintf("%v: %v", b["event"], r)
		}
	}
	return fmt.Sprint(b["event"])
}

func compact(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if k == "idempotency_key" || k == "config_hash" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}
