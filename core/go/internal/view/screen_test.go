package view

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewBlockTypesAndPresentation(t *testing.T) {
	raw := `[
	 {"type":"hero","title":"Rp 12,5 juta","text":"Omzet minggu ini","icon":"📈","emphasis":"strong","say":"Ini omzet minggu ini.","span":"wide"},
	 {"type":"compare","columns":["A","B"],"rows":[["Harga","1","2"],["Rusak","x"]],"text":"B"},
	 {"type":"compare","columns":["A"],"rows":[["Harga","1"]]},
	 {"type":"timeline","items":[{"label":"09.00","value":"Rapat","note":null,"url":null}]},
	 {"type":"progress","items":[{"label":"Selesai","value":"140","note":null,"url":null},{"label":"Sisa","value":"-3","note":null,"url":null}],"span":"half"},
	 {"type":"quote","text":"Sabar itu indah.","title":"Pepatah"},
	 {"type":"form","text":"Cari hotel di {kota} untuk {malam} malam","items":[{"label":"kota","value":null,"note":"nama kota","url":null},
	   {"label":"malam","value":"1|2|3","note":null,"url":null},{"label":"tidak dipakai","value":null,"note":null,"url":null}]},
	 {"type":"form","text":"tanpa isian","items":[{"label":"x","value":null,"note":null,"url":null}]},
	 {"type":"widget","source":"approvals","limit":3}
	]`
	bs, notes := Sanitize(json.RawMessage(raw), Options{MaxBlocks: 12})
	got := map[string]Block{}
	for _, b := range bs {
		got[b.Type] = b
	}
	h := got["hero"]
	if h.Icon != "📈" || h.Emphasis != "strong" || h.Say == "" || h.Span != "" {
		t.Fatalf("hero presentation: %+v", h)
	}
	c := got["compare"]
	if len(c.Columns) != 2 || len(c.Rows) != 2 || c.Rows[1][2] != "" || c.Text != "B" {
		t.Fatalf("compare pads short rows and keeps and a real recommendation: %+v", c)
	}
	p := got["progress"]
	if len(p.Items) != 2 || *p.Items[0].Number != 100 || *p.Items[1].Number != 0 || p.Span != "half" {
		t.Fatalf("progress clamps to 0..100: %+v", p)
	}
	f := got["form"]
	if len(f.Items) != 2 || f.Items[1].Value != "1|2|3" {
		t.Fatalf("form keeps only fields its template uses: %+v", f)
	}
	if _, ok := got["widget"]; ok {
		t.Fatal("a reply view must not contain widgets")
	}
	if _, ok := got["timeline"]; !ok || got["quote"].Title != "Pepatah" {
		t.Fatalf("timeline/quote missing: %+v", bs)
	}
	if len(notes) < 3 { // one-column compare, form without fields, widget
		t.Fatalf("dropped blocks must be reported: %v", notes)
	}
}

func TestSayAndIconAreBounded(t *testing.T) {
	raw := `[{"type":"text","text":"x","say":"` + strings.Repeat("kata ", 200) + `","icon":"<script>alert(1)</script>"}]`
	bs, _ := Sanitize(json.RawMessage(raw), Options{})
	if len(bs) != 1 || len([]rune(bs[0].Say)) > MaxSay+1 || bs[0].Icon != "" {
		t.Fatalf("say/icon not bounded: %+v", bs)
	}
}

func TestSceneKeepsOnlyKnownValues(t *testing.T) {
	s := SanitizeScene(json.RawMessage(`{"mood":"Celebrate","accent":"url(javascript:1)","layout":"story"}`))
	if s != (Scene{Mood: "celebrate", Layout: "story"}) {
		t.Fatalf("scene = %+v", s)
	}
	if SanitizeScene(json.RawMessage(`"x"`)) != (Scene{}) || SanitizeScene(nil) != (Scene{}) {
		t.Fatal("a malformed scene must be empty")
	}
}

func TestScreenAllowsOnlyKnownWidgets(t *testing.T) {
	blocks := `[{"type":"widget","source":"approvals","limit":99,"title":"Persetujuan","icon":"✅"},
	 {"type":"widget","source":"https://evil.example/data"},
	 {"type":"widget","source":"select * from secrets"},
	 {"type":"quote","text":"Bismillah."},
	 {"type":"links","items":[{"label":"x","value":null,"note":null,"url":"https://evil.example"}]},
	 {"type":"html","text":"<b>x</b>"}]`
	sc, notes := SanitizeScreen(json.RawMessage(`{"mood":"calm","accent":"emerald","layout":"grid"}`), json.RawMessage(blocks))
	if len(sc.Blocks) != 2 || sc.Blocks[0].Source != "approvals" || sc.Blocks[0].Limit != 0 || sc.Blocks[1].Type != "quote" {
		t.Fatalf("screen blocks: %+v", sc.Blocks)
	}
	if sc.Scene.Accent != "emerald" || len(notes) != 5 {
		t.Fatalf("scene %+v notes %v", sc.Scene, notes)
	}
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, `{"type":"widget","source":"recent"}`)
	}
	sc, _ = SanitizeScreen(nil, json.RawMessage("["+strings.Join(many, ",")+"]"))
	if len(sc.Blocks) != MaxScreenBlocks {
		t.Fatalf("screen not bounded: %d", len(sc.Blocks))
	}
}

func TestScreenSchemaIsValidJSONAndDoesNotChangeTheViewSchema(t *testing.T) {
	b, err := json.Marshal(ScreenSchema())
	if err != nil || !strings.Contains(string(b), `"widget"`) || !strings.Contains(string(b), `"source"`) {
		t.Fatalf("screen schema: %v %s", err, b)
	}
	v, _ := json.Marshal(Schema())
	if strings.Contains(string(v), `"widget"`) || strings.Contains(string(v), `"source"`) {
		t.Fatal("the reply view schema must not offer widgets")
	}
	if _, err := json.Marshal(SceneSchema()); err != nil {
		t.Fatal(err)
	}
}
