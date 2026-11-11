package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

// designModel answers plan, design and reply calls; everything else like the RS-1 model.
func designModel(plan string, design func(user string) string, planned, replied *bool) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		sys, user := req.Messages[0].Content, req.Messages[len(req.Messages)-1].Content
		switch {
		case strings.Contains(sys, "interface designer of AURA"):
			return gatewaytest.Reply{Content: design(user)}
		case purposeOf(req) == "plan":
			*planned = true
			return gatewaytest.Reply{Content: plan}
		case purposeOf(req) == "reply":
			*replied = true
			return gatewaytest.Reply{Content: replyJSON("x")}
		}
		return gatewaytest.RS1Model(r, req)
	}
}

func designJSON(summary string, scene map[string]any, blocks ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"summary": summary, "scene": scene, "blocks": blocks})
	return string(b)
}

func widget(source string, set map[string]any) map[string]any {
	b := nullBlock("widget", map[string]any{"source": source, "limit": nil})
	for k, v := range set {
		b[k] = v
	}
	return b
}

// ADR-0060: a redesign request is an owner task. The design is data: the Core
// keeps only known widgets and block types, stores it as a new version, tells
// the owner what changed, and the owner can roll back.
func TestScreenDesignIsSanitisedVersionedAndReversible(t *testing.T) {
	var planned, replied bool
	var designUser string
	h := newHarness(t, harnessOpts{replies: true, respond: designModel(noSteps, func(user string) string {
		designUser = user
		return designJSON("Persetujuan sekarang paling atas, Tuan.", map[string]any{"mood": "focus", "accent": "emerald", "layout": nil},
			widget("approvals", map[string]any{"limit": 5, "title": "Perlu Tuan", "emphasis": "strong"}),
			widget("https://evil.example/feed", nil),
			nullBlock("links", map[string]any{"items": []any{nullItem("<script>x</script>", "", "https://evil.example/x")}}),
			nullBlock("quote", map[string]any{"text": "Kerja tuntas, hati tenang."}),
			widget("ask", nil))
	}, &planned, &replied)})
	ctx := context.Background()
	def, err := h.eng.ActiveScreen(ctx, "home")
	if err != nil || def.Version != 0 || len(def.Screen.Blocks) == 0 {
		t.Fatalf("default screen: %+v %v", def, err)
	}
	tk, _, err := h.eng.Submit(ctx, CreateInput{Objective: "Taruh persetujuan paling atas di beranda", AllowDomains: []string{h.host}, Design: "home"})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	if planned || replied {
		t.Fatalf("a design task needs neither a plan nor a written reply (planned=%v replied=%v)", planned, replied)
	}
	if !strings.Contains(designUser, "Taruh persetujuan") || !strings.Contains(designUser, "approvals:") || !strings.Contains(designUser, `"greeting"`) {
		t.Fatalf("designer prompt lacks request, sources or the current design:\n%s", designUser)
	}
	v, _ := h.eng.ActiveScreen(ctx, "home")
	if v.Version != 1 || len(v.Screen.Blocks) != 3 || v.Screen.Blocks[0].Source != "approvals" || v.Screen.Blocks[0].Limit != 5 ||
		v.Screen.Blocks[1].Type != "quote" || v.Screen.Blocks[2].Source != "ask" {
		t.Fatalf("stored design: %+v", v)
	}
	if v.Screen.Scene.Accent != "emerald" || v.Screen.Scene.Layout != "" || v.TaskID == nil || *v.TaskID != tk.ID {
		t.Fatalf("scene/task: %+v", v)
	}
	spec, _ := json.Marshal(v.Screen)
	if strings.Contains(string(spec), "evil") || strings.Contains(string(spec), "script") {
		t.Fatalf("unsafe content stored: %s", spec)
	}
	r, _ := h.eng.GetReply(ctx, tk.ID)
	if r == nil || r.Text != "Persetujuan sekarang paling atas, Tuan." || !hasNote(r, "screen_designed") || !hasNote(r, "view_trimmed") {
		t.Fatalf("reply: %+v", r)
	}
	var data string
	h.st.R.QueryRow("select data_json from task_events where task_id=? and type='SCREEN_DESIGNED'", tk.ID).Scan(&data)
	if !strings.Contains(data, `"version":1`) || strings.Contains(data, "Kerja tuntas") {
		t.Fatalf("timeline event must carry metadata only: %s", data)
	}

	// A second design, then back.
	tk2, _, _ := h.eng.Submit(ctx, CreateInput{Objective: "Sekali lagi", AllowDomains: []string{h.host}, Design: "home"})
	h.wait(tk2.ID, task.Succeeded)
	if v, _ := h.eng.ActiveScreen(ctx, "home"); v.Version != 2 {
		t.Fatalf("version = %d", v.Version)
	}
	if v, err := h.eng.RollbackScreen(ctx, "home"); err != nil || v.Version != 1 {
		t.Fatalf("rollback: %+v %v", v, err)
	}
	if v, err := h.eng.RollbackScreen(ctx, "home"); err != nil || v.Version != 0 || v.Screen.Blocks[0].Source != "greeting" {
		t.Fatalf("rollback to default: %+v %v", v, err)
	}
	if v, err := h.eng.UseScreen(ctx, "home", 2); err != nil || v.Version != 2 {
		t.Fatalf("use: %+v %v", v, err)
	}
	if _, err := h.eng.UseScreen(ctx, "home", 9); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
	vs, _ := h.eng.ScreenVersions(ctx, "home")
	active := 0
	for _, x := range vs {
		if x.Active {
			active++
		}
	}
	if len(vs) != 2 || active != 1 {
		t.Fatalf("versions %d active %d", len(vs), active)
	}
	if _, _, err := h.eng.Submit(ctx, CreateInput{Objective: "x", Design: "settings; drop table"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown screen accepted: %v", err)
	}
}

// The owner can also just say it: the planner names the screen, no tool runs.
func TestPlannerRoutesARedesignRequestSaidInConversation(t *testing.T) {
	var planned, replied bool
	h := newHarness(t, harnessOpts{replies: true, respond: designModel(
		`{"steps":[],"completion_criteria":[],"question":null,"design":"home"}`,
		func(string) string {
			return designJSON("Beranda kini lebih ringkas.", nil, widget("greeting", nil), widget("recent", map[string]any{"limit": 3}))
		}, &planned, &replied)})
	tk := h.submit("Tolong buat beranda lebih ringkas", nil)
	h.wait(tk.ID, task.Succeeded)
	v, _ := h.eng.ActiveScreen(context.Background(), "home")
	if !planned || replied || v.Version != 1 || len(v.Screen.Blocks) != 2 {
		t.Fatalf("planned=%v replied=%v screen=%+v", planned, replied, v)
	}
}

// A design with nothing usable changes nothing and fails honestly.
func TestUnusableDesignLeavesTheScreenAlone(t *testing.T) {
	var planned, replied bool
	h := newHarness(t, harnessOpts{replies: true, respond: designModel(noSteps, func(string) string {
		return designJSON("Sudah.", nil, widget("file:///etc/passwd", nil))
	}, &planned, &replied)})
	tk, _, _ := h.eng.Submit(context.Background(), CreateInput{Objective: "Ubah beranda", AllowDomains: []string{h.host}, Design: "home"})
	h.wait(tk.ID, task.Failed)
	if v, _ := h.eng.ActiveScreen(context.Background(), "home"); v.Version != 0 {
		t.Fatalf("screen changed: %+v", v)
	}
}

func TestHomeContextComesFromTheCoreRecords(t *testing.T) {
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(func(string) string { return noSteps },
		func(string) gatewaytest.Reply { return gatewaytest.Reply{Content: replyJSON("Halo.")} })})
	c := h.eng.Home(context.Background())
	if c.Headline != "idle" || c.Scene.Mood != "calm" || c.Scene.Accent == "" || c.DayPart == "" || c.Providers == nil {
		t.Fatalf("idle context: %+v", c)
	}
	tk := h.submit("Halo", nil)
	h.wait(tk.ID, task.Succeeded)
	if c := h.eng.Home(context.Background()); c.DoneToday != 1 || c.Running != 0 {
		t.Fatalf("after one task: %+v", c)
	}
	for hr, want := range map[int]string{3: "night", 5: "dawn", 9: "morning", 12: "midday", 16: "afternoon", 19: "evening", 23: "night"} {
		if got := dayPart(hr); got != want {
			t.Fatalf("dayPart(%d) = %s", hr, got)
		}
	}
}
