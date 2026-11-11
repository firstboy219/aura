package view

import (
	"encoding/json"
	"testing"
)

func TestScheduleBlockKeepsReadableTimesOnly(t *testing.T) {
	raw := `[{"type":"schedule","items":[
		{"label":"minum obat","value":"2026-11-11T15:00","note":null},
		{"label":"ringkas berita","value":"daily 05:45","note":"task"},
		{"label":"aneh","value":"daily 05:45","note":"exec"},
		{"label":"tanpa waktu","value":"nanti sore","note":"remind"},
		{"label":"","value":"daily 07:00","note":"remind"}]},
		{"type":"schedule","items":[{"label":"x","value":"kapan-kapan"}]}]`
	out, removed := Sanitize(json.RawMessage(raw), Options{})
	if len(out) != 1 || len(out[0].Items) != 3 || len(removed) == 0 {
		t.Fatalf("%+v removed=%v", out, removed)
	}
	it := out[0].Items
	if it[0].Note != "remind" || it[1].Note != "task" || it[2].Note != "remind" || it[1].Value != "daily 05:45" {
		t.Fatalf("%+v", it)
	}
}
