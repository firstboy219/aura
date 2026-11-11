// Package view validates the adaptive view of an owner reply (ADR-0055).
//
// A view is a short list of declarative blocks (text, table, chart, steps,
// actions, …) that a model proposes for the task at hand and every client
// draws with its own native components. It is data, never code: there is no
// HTML, no script, no style and no remote resource in it, so untrusted model
// output cannot run anything in the owner's browser or phone. The Core
// sanitises every view before it is stored; clients only ever see the
// sanitised form.
package view

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Limits. A view is a summary for a screen, not a document store.
const (
	MaxBlocks  = 12
	MaxText    = 4000
	MaxTitle   = 120
	MaxItems   = 30
	MaxRows    = 50
	MaxColumns = 8
	MaxCell    = 300
	MaxLabel   = 160
	MaxAction  = 500
	MaxSay     = 320
	MaxFields  = 6
	MaxFigures = 6
	MaxNotes   = 10
	MaxNote    = 500
	MaxQueue   = 12
)

// Types are the block types every client implements.
var Types = []string{"text", "callout", "metrics", "kv", "table", "list", "steps", "chart", "links", "code", "actions",
	"hero", "timeline", "progress", "compare", "quote", "form", "figures", "note", "player"}

var tones = map[string]bool{"info": true, "success": true, "warning": true, "danger": true}
var charts = map[string]bool{"bar": true, "line": true, "pie": true}
var spans = map[string]bool{"full": true, "half": true}
var emphases = map[string]bool{"normal": true, "strong": true, "quiet": true}

// Item is one entry of a metrics, kv, list, steps, chart, links or actions block.
type Item struct {
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
	Note  string `json:"note,omitempty"`
	URL   string `json:"url,omitempty"`
	// Number is the numeric value of a chart point, parsed by the Core.
	Number *float64 `json:"number,omitempty"`
	// Image is the id of a picture the Core found for a figure (ADR-0061);
	// a model never sets it. The client reads it from GET /media/{id}.
	Image string `json:"image,omitempty"`
}

// Block is one sanitised view block.
type Block struct {
	Type    string     `json:"type"`
	Title   string     `json:"title,omitempty"`
	Text    string     `json:"text,omitempty"`
	Tone    string     `json:"tone,omitempty"`
	Chart   string     `json:"chart,omitempty"`
	Columns []string   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
	Items   []Item     `json:"items,omitempty"`
	// Presentation (ADR-0060). Say is what AURA speaks while the block is on
	// screen; Icon is one emoji; Span places two "half" blocks side by side;
	// Emphasis makes a block prominent or subdued.
	Say      string `json:"say,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Span     string `json:"span,omitempty"`
	Emphasis string `json:"emphasis,omitempty"`
	// Source and Limit belong to a screen's widget block (screen.go).
	Source string `json:"source,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// raw mirrors the flat, all-nullable form models are asked to produce.
type rawItem struct {
	Label *string `json:"label"`
	Value *string `json:"value"`
	Note  *string `json:"note"`
	URL   *string `json:"url"`
}

type rawBlock struct {
	Type    string     `json:"type"`
	Title   *string    `json:"title"`
	Text    *string    `json:"text"`
	Tone    *string    `json:"tone"`
	Chart   *string    `json:"chart"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Items   []rawItem  `json:"items"`
	Say     *string    `json:"say"`
	Icon    *string    `json:"icon"`
	Span    *string    `json:"span"`
	Emph    *string    `json:"emphasis"`
	Source  *string    `json:"source"`
	Limit   *float64   `json:"limit"`
}

// Options control sanitising.
type Options struct {
	// KnownURL reports whether a link target has provenance (it appeared in the
	// objective or in something the task read). Links without provenance are
	// dropped: a model may not invent where the owner is sent. Nil drops all links.
	KnownURL func(string) bool
	// Widgets allows widget blocks (a screen's live-data blocks). A reply has none.
	Widgets bool
	// MaxBlocks overrides the block limit (0 = MaxBlocks).
	MaxBlocks int
}

// Sanitize parses a model-proposed view and returns the blocks that are safe
// and well-formed, plus a note for each thing it removed. Unparseable input
// yields no blocks and one note; it is never an error for the task.
func Sanitize(raw json.RawMessage, o Options) ([]Block, []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var in []rawBlock
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, []string{"view was not a list of blocks"}
	}
	var out []Block
	var notes []string
	drop := func(i int, why string) { notes = append(notes, "block "+strconv.Itoa(i+1)+": "+why) }
	limit := MaxBlocks
	if o.MaxBlocks > 0 {
		limit = o.MaxBlocks
	}
	for i, rb := range in {
		if len(out) >= limit {
			notes = append(notes, "more than "+strconv.Itoa(limit)+" blocks; the rest were dropped")
			break
		}
		b := Block{Type: strings.ToLower(strings.TrimSpace(rb.Type)), Title: clean(deref(rb.Title), MaxTitle, false)}
		b.Say = clean(deref(rb.Say), MaxSay, false)
		b.Icon = icon(deref(rb.Icon))
		if s := strings.ToLower(deref(rb.Span)); spans[s] && s != "full" {
			b.Span = s
		}
		if s := strings.ToLower(deref(rb.Emph)); emphases[s] && s != "normal" {
			b.Emphasis = s
		}
		switch b.Type {
		case "widget":
			if !o.Widgets {
				drop(i, "widget outside a screen")
				continue
			}
			b.Source = strings.ToLower(strings.TrimSpace(deref(rb.Source)))
			if !widgetSources[b.Source] {
				drop(i, "unknown widget source "+strconv.Quote(clean(b.Source, 30, false)))
				continue
			}
			if rb.Limit != nil && *rb.Limit >= 1 && *rb.Limit <= 20 {
				b.Limit = int(*rb.Limit)
			}
		case "hero":
			b.Text = clean(deref(rb.Text), 400, true)
			b.Tone = strings.ToLower(deref(rb.Tone))
			if !tones[b.Tone] {
				b.Tone = ""
			}
			if b.Title == "" {
				b.Title, b.Text = clean(b.Text, MaxTitle, false), ""
			}
			if b.Title == "" {
				drop(i, "empty hero")
				continue
			}
		case "quote":
			b.Text = clean(deref(rb.Text), MaxText, true)
			if b.Text == "" {
				drop(i, "empty quote")
				continue
			}
		case "compare":
			// columns are the options compared; each row is a criterion followed by one cell per option;
			// text optionally names the recommended option.
			for _, c := range rb.Columns {
				if c = clean(c, MaxLabel, false); c != "" && len(b.Columns) < 4 {
					b.Columns = append(b.Columns, c)
				}
			}
			if len(b.Columns) < 2 {
				drop(i, "compare needs 2-4 options")
				continue
			}
			for _, r := range rb.Rows {
				if len(b.Rows) >= 12 || len(r) == 0 {
					continue
				}
				row := make([]string, len(b.Columns)+1)
				for j := range row {
					if j < len(r) {
						row[j] = clean(r[j], MaxCell, false)
					}
				}
				if row[0] != "" {
					b.Rows = append(b.Rows, row)
				}
			}
			if len(b.Rows) == 0 {
				drop(i, "compare without criteria")
				continue
			}
			pick := clean(deref(rb.Text), MaxLabel, false)
			for _, c := range b.Columns {
				if strings.EqualFold(c, pick) {
					b.Text = c // only an option that exists can be recommended
				}
			}
		case "form":
			// text is the request sent when the owner submits, with {field} placeholders; each item is a field:
			// label = field name, value = choices separated by "|" (empty = free text), note = hint.
			b.Text = clean(deref(rb.Text), MaxAction, false)
			for _, ri := range rb.Items {
				it := Item{Label: clean(deref(ri.Label), 40, false), Value: clean(deref(ri.Value), MaxCell, false), Note: clean(deref(ri.Note), MaxLabel, false)}
				if it.Label == "" || strings.ContainsAny(it.Label, "{}") || len(b.Items) >= MaxFields || !strings.Contains(b.Text, "{"+it.Label+"}") {
					continue // a field the request does not use is not asked
				}
				b.Items = append(b.Items, it)
			}
			if b.Text == "" || len(b.Items) == 0 {
				drop(i, "form without a request or fields")
				continue
			}
		case "figures":
			// the people, companies or things an explanation is about: label = the name (the Core looks for
			// its picture), value = what it did or what happened to it, note = the consequence.
			for _, ri := range rb.Items {
				it := Item{Label: clean(deref(ri.Label), 80, false), Value: clean(deref(ri.Value), MaxCell, false), Note: clean(deref(ri.Note), MaxCell, false)}
				if it.Label != "" && len(b.Items) < MaxFigures {
					b.Items = append(b.Items, it)
				}
			}
			if len(b.Items) == 0 {
				drop(i, "figures without names")
				continue
			}
		case "note":
			// notes the owner asked to keep: each item's label is one note. The Core saves them (ADR-0061).
			// Without items the block only opens the owner's notes.
			for _, ri := range rb.Items {
				if n := clean(deref(ri.Label), MaxNote, false); n != "" && len(b.Items) < MaxNotes {
					b.Items = append(b.Items, Item{Label: n})
				}
			}
			if n := clean(deref(rb.Text), MaxNote, false); n != "" && len(b.Items) == 0 {
				b.Items = append(b.Items, Item{Label: n})
			}
		case "player":
			// music on the owner's device: text = what to look for, items = the songs wanted (label = title,
			// value = artist). The device plays only what its own library has.
			b.Text = clean(deref(rb.Text), MaxLabel, false)
			for _, ri := range rb.Items {
				it := Item{Label: clean(deref(ri.Label), MaxLabel, false), Value: clean(deref(ri.Value), MaxLabel, false)}
				if it.Label != "" && len(b.Items) < MaxQueue {
					b.Items = append(b.Items, it)
				}
			}
		case "text", "code":
			b.Text = clean(deref(rb.Text), MaxText, true)
			if b.Text == "" {
				drop(i, "empty "+b.Type)
				continue
			}
		case "callout":
			b.Text = clean(deref(rb.Text), MaxText, true)
			b.Tone = strings.ToLower(deref(rb.Tone))
			if !tones[b.Tone] {
				b.Tone = "info"
			}
			if b.Text == "" {
				drop(i, "empty callout")
				continue
			}
		case "table":
			for _, c := range rb.Columns {
				if len(b.Columns) < MaxColumns {
					b.Columns = append(b.Columns, clean(c, MaxLabel, false))
				}
			}
			if len(b.Columns) == 0 {
				drop(i, "table without columns")
				continue
			}
			for _, r := range rb.Rows {
				if len(b.Rows) >= MaxRows {
					notes = append(notes, "block "+strconv.Itoa(i+1)+": rows beyond "+strconv.Itoa(MaxRows)+" were dropped")
					break
				}
				row := make([]string, len(b.Columns))
				for j := range row {
					if j < len(r) {
						row[j] = clean(r[j], MaxCell, false)
					}
				}
				b.Rows = append(b.Rows, row)
			}
			if len(b.Rows) == 0 {
				drop(i, "table without rows")
				continue
			}
		case "metrics", "kv", "list", "steps", "chart", "links", "actions", "timeline", "progress":
			if b.Type == "chart" {
				b.Chart = strings.ToLower(deref(rb.Chart))
				if !charts[b.Chart] {
					b.Chart = "bar"
				}
			}
			for _, ri := range rb.Items {
				if len(b.Items) >= MaxItems {
					break
				}
				it := Item{Label: clean(deref(ri.Label), MaxLabel, false), Value: clean(deref(ri.Value), MaxCell, false),
					Note: clean(deref(ri.Note), MaxCell, false)}
				switch b.Type {
				case "chart":
					n, ok := Number(it.Value)
					if !ok || it.Label == "" {
						continue // a chart point needs a label and a number
					}
					it.Number = &n
				case "links":
					u, ok := safeURL(deref(ri.URL))
					if !ok || o.KnownURL == nil || !o.KnownURL(u) {
						notes = append(notes, "block "+strconv.Itoa(i+1)+": a link without provenance was dropped")
						continue
					}
					it.URL = u
					if it.Label == "" {
						it.Label = u
					}
				case "actions":
					// label is the button text, value the follow-up request the owner may send.
					it.Value = clean(deref(ri.Value), MaxAction, false)
					if it.Label == "" || it.Value == "" {
						continue
					}
				case "metrics", "kv", "timeline":
					if it.Label == "" || it.Value == "" {
						continue
					}
				case "progress":
					n, ok := Number(it.Value)
					if !ok || it.Label == "" {
						continue
					}
					n = max(0, min(100, n)) // a share of the whole, in percent
					it.Number = &n
				default: // list, steps
					if it.Label == "" {
						it.Label, it.Value = it.Value, ""
					}
					if it.Label == "" {
						continue
					}
				}
				b.Items = append(b.Items, it)
			}
			min := 1
			if b.Type == "chart" {
				min = 2
			}
			if len(b.Items) < min {
				drop(i, b.Type+" without usable items")
				continue
			}
		default:
			drop(i, "unknown block type "+strconv.Quote(clean(rb.Type, 30, false)))
			continue
		}
		out = append(out, b)
	}
	return out, notes
}

// Number parses a displayed number ("1.250,5", "12%", "Rp 3.000", "1,234.5").
func Number(s string) (float64, bool) {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) || r == '.' || r == ',' || r == '-' {
			b.WriteRune(r)
		}
	}
	t := b.String()
	if t == "" {
		return 0, false
	}
	dot, comma := strings.LastIndex(t, "."), strings.LastIndex(t, ",")
	switch {
	case dot >= 0 && comma >= 0:
		if comma > dot { // 1.250,5
			t = strings.ReplaceAll(t, ".", "")
			t = strings.Replace(t, ",", ".", 1)
		} else { // 1,250.5
			t = strings.ReplaceAll(t, ",", "")
		}
	case comma >= 0:
		if strings.Count(t, ",") > 1 || len(t)-comma-1 == 3 {
			t = strings.ReplaceAll(t, ",", "") // thousands
		} else {
			t = strings.Replace(t, ",", ".", 1)
		}
	case dot >= 0:
		if strings.Count(t, ".") > 1 {
			t = strings.ReplaceAll(t, ".", "") // 1.250.000
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || n != n || n > 1e15 || n < -1e15 {
		return 0, false
	}
	return n, true
}

// icon keeps a single short symbol (an emoji or a letter): no text hides in it.
func icon(s string) string {
	s = clean(s, 8, false)
	if strings.ContainsAny(s, " <>&\"'") || len([]rune(s)) > 6 {
		return ""
	}
	for _, r := range s {
		if r < 128 && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return ""
		}
	}
	return s
}

func safeURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(s) > 2000 {
		return "", false
	}
	return u.String(), true
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// clean removes control characters, trims, and cuts to n runes. Newlines and
// tabs survive only in multi-line fields.
func clean(s string, n int, multiline bool) string {
	var b strings.Builder
	count := 0
	for _, r := range s {
		if r == '\n' || r == '\t' {
			if !multiline {
				r = ' '
			}
		} else if unicode.IsControl(r) || r == ' ' || r == ' ' || r == '‮' || r == '‭' {
			continue
		}
		if count >= n {
			b.WriteRune('…')
			break
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

// CleanText sanitises the plain reply text with the same rules.
func CleanText(s string, n int) string { return clean(s, n, true) }

// Schema is the JSON Schema of a model-proposed view (flat and all-nullable,
// which strict structured-output modes accept).
func Schema() map[string]any {
	ns := map[string]any{"type": []string{"string", "null"}}
	item := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"label", "value", "note", "url"},
		"properties": map[string]any{"label": ns, "value": ns, "note": ns, "url": ns}}
	block := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"type", "title", "text", "tone", "chart", "columns", "rows", "items", "say", "icon", "span", "emphasis"},
		"properties": map[string]any{
			"type": map[string]any{"type": "string", "enum": Types}, "title": ns, "text": ns, "tone": ns, "chart": ns,
			"say": ns, "icon": ns, "span": ns, "emphasis": ns,
			"columns": map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "string"}},
			"rows":    map[string]any{"type": []string{"array", "null"}, "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			"items":   map[string]any{"type": []string{"array", "null"}, "items": item}}}
	return map[string]any{"type": "array", "items": block}
}
