package view

import (
	"encoding/json"
	"strings"
	"testing"
)

func s(v string) *string { return &v }

func TestSanitizeKeepsWellFormedBlocks(t *testing.T) {
	in := []rawBlock{
		{Type: "callout", Text: s("Selesai"), Tone: s("success")},
		{Type: "table", Columns: []string{"Kota", "Suhu"}, Rows: [][]string{{"Jakarta", "31"}, {"Bandung"}}},
		{Type: "chart", Chart: s("pie"), Items: []rawItem{{Label: s("A"), Value: s("1.250,5")}, {Label: s("B"), Value: s("40%")}}},
		{Type: "metrics", Items: []rawItem{{Label: s("Total"), Value: s("Rp 3.000")}}},
		{Type: "steps", Items: []rawItem{{Value: s("Buka aplikasi")}}},
		{Type: "actions", Items: []rawItem{{Label: s("Detail"), Value: s("Jelaskan lebih detail")}}},
	}
	raw, _ := json.Marshal(in)
	out, notes := Sanitize(raw, Options{})
	if len(out) != 6 || len(notes) != 0 {
		t.Fatalf("blocks %d notes %v", len(out), notes)
	}
	if out[1].Rows[1][1] != "" || len(out[1].Rows[1]) != 2 {
		t.Fatalf("short row not padded: %v", out[1].Rows)
	}
	if *out[2].Items[0].Number != 1250.5 || *out[2].Items[1].Number != 40 {
		t.Fatalf("chart numbers: %v %v", *out[2].Items[0].Number, *out[2].Items[1].Number)
	}
	if out[4].Items[0].Label != "Buka aplikasi" {
		t.Fatalf("step label: %+v", out[4].Items[0])
	}
}

// A view is data: nothing in it may become markup, and anything malformed is
// removed with a note rather than shown.
func TestSanitizeDropsUnsafeAndMalformed(t *testing.T) {
	in := []map[string]any{
		{"type": "html", "text": "<script>alert(1)</script>"},
		{"type": "text", "text": "ok\u0000‮<b>bold</b>"},
		{"type": "chart", "items": []map[string]any{{"label": "A", "value": "banyak"}, {"label": "B", "value": "2"}}},
		{"type": "links", "items": []map[string]any{
			{"label": "known", "url": "https://example.org/a"},
			{"label": "invented", "url": "https://evil.example/x"},
			{"label": "js", "url": "javascript:alert(1)"},
			{"label": "cred", "url": "https://user:pw@example.org/a"}}},
		{"type": "table", "columns": []string{}, "rows": [][]string{{"x"}}},
		{"type": "callout", "text": "hi", "tone": "blink"},
	}
	raw, _ := json.Marshal(in)
	out, notes := Sanitize(raw, Options{KnownURL: func(u string) bool { return u == "https://example.org/a" }})
	if len(out) != 3 {
		t.Fatalf("want text, links, callout; got %+v", out)
	}
	if out[0].Type != "text" || out[0].Text != "ok<b>bold</b>" { // kept as literal text; clients never render markup
		t.Fatalf("text: %q", out[0].Text)
	}
	if out[1].Type != "links" || len(out[1].Items) != 1 || out[1].Items[0].URL != "https://example.org/a" {
		t.Fatalf("links: %+v", out[1])
	}
	if out[2].Tone != "info" {
		t.Fatalf("tone: %q", out[2].Tone)
	}
	if len(notes) < 4 {
		t.Fatalf("removals must be reported: %v", notes)
	}
	if _, n := Sanitize(json.RawMessage(`{"type":"text"}`), Options{}); len(n) != 1 {
		t.Fatalf("non-list view: %v", n)
	}
}

func TestSanitizeLimits(t *testing.T) {
	var in []map[string]any
	for i := 0; i < 30; i++ {
		in = append(in, map[string]any{"type": "text", "text": strings.Repeat("x", 9000)})
	}
	raw, _ := json.Marshal(in)
	out, notes := Sanitize(raw, Options{})
	if len(out) != MaxBlocks || len(notes) != 1 {
		t.Fatalf("blocks %d notes %v", len(out), notes)
	}
	if n := len([]rune(out[0].Text)); n > MaxText+1 {
		t.Fatalf("text not cut: %d", n)
	}
}

func TestNumber(t *testing.T) {
	for in, want := range map[string]float64{"12": 12, "1,234.5": 1234.5, "1.250,5": 1250.5, "1.250.000": 1250000, "3,5": 3.5, "1,000": 1000, "-4.2%": -4.2, "Rp 3.000": 3} {
		if got, ok := Number(in); !ok || got != want {
			t.Errorf("%q: got %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := Number("n/a"); ok {
		t.Error("n/a parsed")
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	if _, err := json.Marshal(Schema()); err != nil {
		t.Fatal(err)
	}
}
