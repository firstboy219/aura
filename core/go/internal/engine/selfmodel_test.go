package engine

import (
	"context"
	"testing"

	"aura.local/core/internal/task"
)

func TestSelfModelScorecardShadow(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		tk := h.submit(rs1Objective(h.host), nil)
		h.wait(tk.ID, task.Succeeded)
		waitAnyEvent(t, h, tk.ID, "REFLECTED", "REFLECTION_SKIPPED")
	}
	var purposes, nullPurpose int
	h.st.R.QueryRow("select count(distinct purpose), sum(purpose is null) from usage_records").Scan(&purposes, &nullPurpose)
	if purposes < 3 || nullPurpose != 0 {
		t.Fatalf("purposes recorded: %d distinct, %d missing", purposes, nullPurpose)
	}
	sm, err := h.eng.SelfModel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tasks := sm["tasks"].(map[string]any)
	if tasks["succeeded"].(int) != 2 || tasks["verified"].(int) != 2 {
		t.Fatalf("%v", tasks)
	}
	if tools := sm["tools"].(map[string]map[string]int); tools["web.fetch"]["ok"] != 6 {
		t.Fatalf("tools %v", tools)
	}
	sc, _ := h.eng.Scorecard(ctx)
	if sc["verified_success_rate"].(float64) != 1 || sc["energy"] == nil {
		t.Fatalf("%v", sc)
	}
	var shadow int
	h.st.R.QueryRow("select count(*) from routing_shadow").Scan(&shadow)
	rep, _ := h.eng.ShadowReport(ctx)
	if shadow == 0 || rep["purposes"] == nil {
		t.Fatalf("shadow decisions %d %v", shadow, rep)
	}
	// Shadow never changes what was used: few samples → shadow equals actual.
	var differ int
	h.st.R.QueryRow("select count(*) from routing_shadow where actual_tier != shadow_tier").Scan(&differ)
	if differ != 0 {
		t.Fatal("shadow policy diverged without enough samples")
	}
}
