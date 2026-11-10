package console

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Console v2 panels (Blueprint §13A.3, P3): memory, skills, self-model, the
// Limitation Ledger and owner notifications. They read the same API as the
// CLI; destructive actions ask for confirmation.

type panelItem struct {
	id   string
	line string
	raw  map[string]any
}

type panelMsg struct {
	panel string
	items []panelItem
}

type panelStatusMsg string

var panelKeys = map[string]string{"m": "memory", "s": "skills", "i": "self", "l": "ledger", "o": "notifications"}

var panelTitles = map[string]string{"memory": "Memory", "skills": "Skills (L1)", "self": "Self-model",
	"ledger": "Limitation Ledger", "notifications": "Notifications"}

var panelHelp = map[string]string{
	"memory":        "↑↓ enter view · d delete · r reload · esc back   (CLI: aura memory list|correct|delete)",
	"skills":        "↑↓ enter view · x disable · e enable · b roll back · esc back   (CLI: aura skill …)",
	"self":          "esc back   (CLI: aura self)",
	"ledger":        "↑↓ enter view · r reload · esc back   (CLI: aura limitations · aura limitation accept|dismiss|reopen <id>)",
	"notifications": "↑↓ enter view (marks read) · r reload · esc back   (CLI: aura notifications)",
}

var panelPaths = map[string]struct{ path, key string }{
	"memory":        {"/memories?limit=100", "memories"},
	"skills":        {"/skills", "skills"},
	"ledger":        {"/limitations", "limitations"},
	"notifications": {"/notifications", "notifications"},
}

func (m *Model) openPanel(p string) tea.Cmd {
	m.stopStream()
	m.panel, m.mode, m.cursor, m.items = p, modePanel, 0, nil
	return m.loadPanel()
}

func (m *Model) loadPanel() tea.Cmd {
	p := m.panel
	return func() tea.Msg {
		if p == "self" {
			raw, err := m.api.Raw("GET", "/self", nil)
			if err != nil {
				return errMsg{err}
			}
			var v map[string]any
			json.Unmarshal(raw, &v)
			return panelMsg{p, []panelItem{{id: "self", line: "AURA self-description (computed from Core records)", raw: v}}}
		}
		spec := panelPaths[p]
		raw, err := m.api.Raw("GET", spec.path, nil)
		if err != nil {
			return errMsg{err}
		}
		var body map[string][]map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			return errMsg{err}
		}
		var items []panelItem
		for _, r := range body[spec.key] {
			items = append(items, panelItem{id: strOf(r["id"]), line: panelLine(p, r), raw: r})
		}
		return panelMsg{p, items}
	}
}

// panelLine is the one-line summary of an item; state is always spelled out
// (no meaning by color alone).
func panelLine(p string, r map[string]any) string {
	switch p {
	case "memory":
		return fmt.Sprintf("%-8s %-9s %-8s %s", strOf(r["kind"]), strOf(r["status"]), strOf(r["privacy"]), oneLine(strOf(r["content"])))
	case "skills":
		return fmt.Sprintf("%-9s v%-3.0f %s — %s", strOf(r["status"]), num(r["current_version"]), strOf(r["name"]), oneLine(strOf(r["notes"])))
	case "ledger":
		return fmt.Sprintf("%-12s %-22s %s", strOf(r["status"]), strOf(r["category"]), oneLine(strOf(r["title"])))
	case "notifications":
		read := "unread"
		if r["read_at"] != nil {
			read = "read"
		}
		return fmt.Sprintf("%-6s %-7s %s %s", read, strOf(r["kind"]), when(r["created_at"]), oneLine(strOf(r["title"])))
	}
	return strOf(r["id"])
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func when(v any) string {
	ms := int64(num(v))
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("01-02 15:04Z")
}

func (m *Model) viewPanel(w int) []string {
	lines := []string{header.Render(clip(fmt.Sprintf(" %s · %d ", panelTitles[m.panel], len(m.items)), w))}
	if len(m.items) == 0 {
		lines = append(lines, dim.Render("  (nothing yet)"))
	}
	for i, it := range m.items {
		mark := "  "
		if i == m.cursor {
			mark = "> "
		}
		lines = append(lines, clip(mark+short(it.id)+" "+it.line, w))
	}
	return lines
}

func (m *Model) current() *panelItem {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	return &m.items[m.cursor]
}

func (m *Model) panelKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == modePanelItem {
		if s := k.String(); s == "esc" || s == "q" {
			m.mode = modePanel
		}
		return m, nil
	}
	it := m.current()
	switch k.String() {
	case "esc", "q":
		m.mode, m.panel, m.items = modeList, "", nil
		m.cursor = 0
		return m, m.loadTasks()
	case "up", "k":
		m.cursor = max(0, m.cursor-1)
	case "down", "j":
		m.cursor = min(len(m.items)-1, m.cursor+1)
	case "r":
		return m, m.loadPanel()
	case "enter":
		if it == nil {
			return m, nil
		}
		b, _ := json.MarshalIndent(it.raw, "", "  ")
		m.itemText, m.mode = string(b), modePanelItem
		if m.panel == "notifications" && it.raw["read_at"] == nil {
			return m, m.panelAction("POST", "/notifications/"+url.PathEscape(it.id)+"/read", nil, "")
		}
	case "d":
		if m.panel == "memory" && it != nil {
			m.prev, m.mode, m.confirmFor, m.confirmID = modePanel, modeConfirm, "delete-memory", it.id
		}
	case "x", "e":
		if m.panel == "skills" && it != nil {
			action := map[string]string{"x": "disable", "e": "enable"}[k.String()]
			return m, m.panelAction("POST", "/skills/"+url.PathEscape(it.id)+"/"+action, nil, "skill "+action+"d")
		}
	case "b":
		if m.panel == "skills" && it != nil {
			if num(it.raw["current_version"]) < 2 {
				m.status = "this skill has no earlier version"
				return m, nil
			}
			m.prev, m.mode, m.confirmFor, m.confirmID = modePanel, modeConfirm, "rollback-skill", it.id
		}
	}
	return m, nil
}

// confirmPanel runs a confirmed destructive panel action.
func (m *Model) confirmPanel() tea.Cmd {
	id := m.confirmID
	switch m.confirmFor {
	case "delete-memory":
		return m.panelAction("DELETE", "/memories/"+url.PathEscape(id), nil, "memory deleted")
	case "rollback-skill":
		for _, it := range m.items {
			if it.id == id {
				v := int(num(it.raw["current_version"])) - 1
				return m.panelAction("POST", "/skills/"+url.PathEscape(id)+"/rollback", map[string]int{"version": v}, fmt.Sprintf("skill rolled back to v%d", v))
			}
		}
	}
	return nil
}

func (m *Model) panelAction(method, path string, body any, ok string) tea.Cmd {
	return func() tea.Msg {
		if _, err := m.api.Raw(method, path, body); err != nil {
			return errMsg{err}
		}
		return panelStatusMsg(ok)
	}
}
