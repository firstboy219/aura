package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

const noSteps = `{"steps":[],"completion_criteria":[],"question":null}`

func purposeOf(req gateway.Request) string {
	sys := req.Messages[0].Content
	switch {
	case strings.Contains(sys, "the voice of AURA"):
		return "reply"
	case strings.Contains(sys, "planning module") && !strings.Contains(sys, "replanning module"):
		return "plan"
	}
	return ""
}

func nullBlock(typ string, set map[string]any) map[string]any {
	b := map[string]any{"type": typ, "title": nil, "text": nil, "tone": nil, "chart": nil, "columns": nil, "rows": nil, "items": nil}
	for k, v := range set {
		b[k] = v
	}
	return b
}

func nullItem(label, value, url string) map[string]any {
	it := map[string]any{"label": nil, "value": nil, "note": nil, "url": nil}
	if label != "" {
		it["label"] = label
	}
	if value != "" {
		it["value"] = value
	}
	if url != "" {
		it["url"] = url
	}
	return it
}

func replyJSON(text string, blocks ...map[string]any) string {
	if blocks == nil {
		blocks = []map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"text": text, "view": blocks})
	return string(b)
}

// replyModel answers plan/reply calls from the given functions and everything else like the RS-1 model.
func replyModel(plan func(user string) string, reply func(user string) gatewaytest.Reply) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		user := req.Messages[len(req.Messages)-1].Content
		switch purposeOf(req) {
		case "plan":
			if plan != nil {
				return gatewaytest.Reply{Content: plan(user)}
			}
		case "reply":
			return reply(user)
		}
		return gatewaytest.RS1Model(r, req)
	}
}

func hasNote(r *Reply, code string) bool {
	for _, n := range r.Notes {
		if n.Code == code {
			return true
		}
	}
	return false
}

// A question that needs no tool is answered directly, labelled as model knowledge,
// and its view is sanitised by the Core.
func TestDirectAnswerIsLabelledModelKnowledge(t *testing.T) {
	var planPrompt, replyPrompt string
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(
		func(user string) string { planPrompt = user; return noSteps },
		func(user string) gatewaytest.Reply {
			replyPrompt = user
			return gatewaytest.Reply{Content: replyJSON("Halo Tuan, saya AURA.",
				nullBlock("callout", map[string]any{"text": "Siap membantu.", "tone": "success"}),
				nullBlock("links", map[string]any{"items": []any{nullItem("Evil", "", "https://evil.example/x")}}),
				nullBlock("actions", map[string]any{"items": []any{nullItem("Ringkas berita", "Cari dan ringkas berita energi hari ini", "")}}))}
		})})
	tk := h.submit("Halo AURA, siapa kamu?", nil)
	h.wait(tk.ID, task.Succeeded)
	r, err := h.eng.GetReply(context.Background(), tk.ID)
	if err != nil || r == nil {
		t.Fatalf("no reply stored: %v", err)
	}
	if r.Text != "Halo Tuan, saya AURA." || r.Basis != BasisModel || !hasNote(r, "model_knowledge") {
		t.Fatalf("reply = %+v", r)
	}
	if len(r.View) != 2 || r.View[0].Type != "callout" || r.View[1].Type != "actions" || !hasNote(r, "view_trimmed") {
		t.Fatalf("view not sanitised (an invented link must be dropped): %+v notes %+v", r.View, r.Notes)
	}
	if steps, _ := task.Steps(context.Background(), h.st.R, tk.ID); len(steps) != 0 {
		t.Fatalf("direct answer ran %d steps", len(steps))
	}
	if !strings.Contains(planPrompt, "About you") || !strings.Contains(replyPrompt, "no consciousness") {
		t.Fatalf("self brief missing from prompts")
	}
	var seen bool
	for _, ev := range h.events(tk.ID) {
		seen = seen || ev.Type == "OWNER_REPLY"
	}
	if !seen {
		t.Fatal("no OWNER_REPLY event")
	}
}

// A task that used tools is answered from its evidence; numbers the Core cannot
// trace are flagged, links must have provenance, written files are listed.
func TestReplyFromEvidenceFlagsUntracedNumbers(t *testing.T) {
	var host string
	h := newHarness(t, harnessOpts{replies: true, noStart: true, respond: replyModel(nil, func(user string) gatewaytest.Reply {
		if !strings.Contains(user, "<untrusted_observation") {
			return gatewaytest.Reply{Content: replyJSON("no evidence was given")}
		}
		return gatewaytest.Reply{Content: replyJSON("Ringkasan selesai; 987654 pembaca.",
			nullBlock("links", map[string]any{"items": []any{nullItem("Solar", "", "http://"+host+"/one"), nullItem("x", "", "http://"+host+"/never-read")}}))}
	})})
	host = h.host
	h.start()
	tk := h.submit(rs1Objective(h.host), nil)
	h.wait(tk.ID, task.Succeeded)
	r, _ := h.eng.GetReply(context.Background(), tk.ID)
	if r == nil || r.Basis != BasisEvidence || !hasNote(r, "ungrounded_numbers") || hasNote(r, "model_knowledge") {
		t.Fatalf("reply = %+v", r)
	}
	if len(r.Files) != 1 || r.Files[0] != "reports/summary.md" {
		t.Fatalf("files = %v", r.Files)
	}
	if len(r.View) != 1 || len(r.View[0].Items) != 1 || !strings.HasSuffix(r.View[0].Items[0].URL, "/one") {
		t.Fatalf("links = %+v", r.View)
	}
}

// When no model can write the reply, a finished tool task still completes: the
// Core composes a plain summary and says so.
func TestReplyWriterFailureFallsBackToCoreSummary(t *testing.T) {
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(nil, func(string) gatewaytest.Reply {
		return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 500, Msg: "down"}}
	})})
	tk := h.submit(rs1Objective(h.host), nil)
	h.wait(tk.ID, task.Succeeded)
	r, _ := h.eng.GetReply(context.Background(), tk.ID)
	if r == nil || r.Basis != BasisSummary || !hasNote(r, "core_summary") || len(r.View) == 0 {
		t.Fatalf("reply = %+v", r)
	}
}

// A direct answer has nothing but its reply: without one the task fails.
func TestDirectAnswerFailsWithoutReply(t *testing.T) {
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(func(string) string { return noSteps }, func(string) gatewaytest.Reply {
		return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 500, Msg: "down"}}
	})})
	tk := h.submit("Apa kabar?", nil)
	got := h.wait(tk.ID, task.Failed)
	if r, _ := h.eng.GetReply(context.Background(), tk.ID); r != nil {
		t.Fatalf("failed task %v has a reply %+v", deref(got.Reason), r)
	}
}

// An objective that names an output or a URL cannot be answered without tools.
func TestNoDirectAnswerWhenWorkIsRequired(t *testing.T) {
	var planPrompt string
	h := newHarness(t, harnessOpts{replies: true, respond: func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		if purposeOf(req) == "plan" {
			planPrompt = req.Messages[0].Content
			return gatewaytest.Reply{Content: noSteps}
		}
		return gatewaytest.RS1Model(r, req)
	}})
	tk := h.submit(rs1Objective(h.host), nil)
	h.wait(tk.ID, task.Failed)
	if !strings.Contains(planPrompt, "Every plan needs at least one step") {
		t.Fatal("planner was offered a direct answer for an objective that names outputs")
	}
	if r, _ := h.eng.GetReply(context.Background(), tk.ID); r != nil {
		t.Fatal("a failed task must not have a reply")
	}
}

// Follow-ups see the recent conversation; a public task never sees private exchanges.
func TestConversationContextRespectsDataClass(t *testing.T) {
	prompts := map[string]string{}
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(
		func(user string) string { prompts[strings.SplitN(user, "\n", 3)[1]] = user; return noSteps },
		func(user string) gatewaytest.Reply {
			return gatewaytest.Reply{Content: replyJSON("Jawaban rahasia-biru.")}
		})})
	first := h.submit("Sebutkan warna favorit saya", nil)
	h.wait(first.ID, task.Succeeded)
	second := h.submit("Jelaskan yang tadi", nil)
	h.wait(second.ID, task.Succeeded)
	if p := prompts["Jelaskan yang tadi"]; !strings.Contains(p, "Sebutkan warna favorit saya") || !strings.Contains(p, "rahasia-biru") {
		t.Fatalf("follow-up did not see the conversation:\n%s", p)
	}
	pub, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: "Pertanyaan publik", AllowDomains: []string{h.host}, DataClass: "public"})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(pub.ID, task.Succeeded)
	if p := prompts["Pertanyaan publik"]; p == "" || strings.Contains(p, "rahasia-biru") || strings.Contains(p, "warna favorit") {
		t.Fatalf("a public task saw private conversation:\n%s", p)
	}
}

// reply "none" turns the reply off for one task.
func TestReplyNone(t *testing.T) {
	h := newHarness(t, harnessOpts{replies: true})
	tk, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: rs1Objective(h.host), AllowDomains: []string{h.host}, Reply: "none"})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	if r, _ := h.eng.GetReply(context.Background(), tk.ID); r != nil {
		t.Fatalf("reply written although turned off: %+v", r)
	}
}

func TestUngroundedNumbers(t *testing.T) {
	ev := []evidenceItem{{Source: "calc", Kind: "observation", Content: "result 1.650.000 and 3.14159"}}
	got := ungroundedNumbers("Total 1,650,000 (pi 3.14), bukan 777888; tahun 2031.", nil, ev, "anggaran 2031")
	if len(got) != 1 || got[0] != "777888" {
		t.Fatalf("got %v", got)
	}
}
