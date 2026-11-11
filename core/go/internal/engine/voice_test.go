package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

func spoken(texts ...string) *task.Voice {
	v := &task.Voice{}
	for i, s := range texts {
		c := 0.6 - 0.1*float64(i)
		v.Alternatives = append(v.Alternatives, task.VoiceAlt{Text: s, Confidence: &c})
	}
	return v
}

// ADR-0058: the planner sees the recognizer's alternatives, repairs what was
// misheard, and every later module works on that understanding. The owner's
// words stay the objective; the timeline records only metadata.
func TestSpokenObjectiveIsUnderstoodAndTheReplyWorksOnTheMeaning(t *testing.T) {
	var planUser, replyUser string
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(
		func(user string) string {
			planUser = user
			return `{"steps":[],"completion_criteria":[],"question":null,"heard_as":"Tolong kirim email ke Pak Budi besok jam 15.00"}`
		},
		func(user string) gatewaytest.Reply {
			replyUser = user
			return gatewaytest.Reply{Content: replyJSON("Baik Tuan, draf email untuk Pak Budi sudah saya siapkan.")}
		})})
	obj := "tolong kirim imel ke pak budi besok jam tiga sore"
	tk, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: obj, AllowDomains: []string{h.host},
		Voice: spoken(obj, "tolong kirim imel ke pak budhi besok jam tiga sore", obj)})
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.Scope.Voice.Alternatives) != 2 {
		t.Fatalf("duplicate alternative kept: %+v", tk.Scope.Voice)
	}
	h.wait(tk.ID, task.Succeeded)
	if !strings.Contains(planUser, "speech recognizer heard") || !strings.Contains(planUser, "pak budhi") {
		t.Fatalf("planner did not see the alternatives:\n%s", planUser)
	}
	if got := h.eng.HeardAs(context.Background(), tk.ID); got != "Tolong kirim email ke Pak Budi besok jam 15.00" {
		t.Fatalf("heard_as = %q", got)
	}
	if !strings.Contains(replyUser, "Pak Budi besok jam 15.00") || !strings.Contains(replyUser, obj) {
		t.Fatalf("the reply writer did not get the understood objective:\n%s", replyUser)
	}
	cur, _ := task.Get(context.Background(), h.st.R, tk.ID)
	if cur.Objective != obj {
		t.Fatalf("the owner's words were replaced: %q", cur.Objective)
	}
	var data string
	h.st.R.QueryRow("select data_json from task_events where task_id=? and type='VOICE_UNDERSTOOD'", tk.ID).Scan(&data)
	if !strings.Contains(data, `"corrected":true`) || strings.Contains(data, "Budi") {
		t.Fatalf("timeline event must carry metadata only: %s", data)
	}
}

// Two plausible readings that lead to different actions: the reply is the
// confirmation question itself, nothing is done, and the task is not parked.
func TestAmbiguousSpokenRequestIsConfirmedNotGuessed(t *testing.T) {
	replied := false
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(
		func(string) string {
			return `{"steps":[],"completion_criteria":[],"question":"Maksud Tuan: transfer lima ratus ribu atau lima juta?","heard_as":"transfer lima ratus ribu"}`
		},
		func(string) gatewaytest.Reply { replied = true; return gatewaytest.Reply{Content: replyJSON("x")} })})
	obj := "transfer lima ratus ribu"
	tk, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: obj, AllowDomains: []string{h.host}, Voice: spoken(obj, "transfer lima juta")})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	r, _ := h.eng.GetReply(context.Background(), tk.ID)
	if r == nil || r.Text != "Maksud Tuan: transfer lima ratus ribu atau lima juta?" || !hasNote(r, "voice_confirm") || replied {
		t.Fatalf("reply = %+v (writer called: %v)", r, replied)
	}
}

// A typed objective is never reinterpreted, even if a model returns heard_as.
func TestTypedObjectiveIgnoresHeardAs(t *testing.T) {
	h := newHarness(t, harnessOpts{replies: true, respond: replyModel(
		func(string) string { return `{"steps":[],"completion_criteria":[],"question":null}` },
		func(string) gatewaytest.Reply { return gatewaytest.Reply{Content: replyJSON("Halo Tuan.")} })})
	tk := h.submit("halo", nil)
	h.wait(tk.ID, task.Succeeded)
	if h.eng.HeardAs(context.Background(), tk.ID) != "" || h.eng.objectiveFor(context.Background(), tk) != "halo" {
		t.Fatal("a typed objective was reinterpreted")
	}
}

func TestVoiceInputIsBounded(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	bad := []*task.Voice{{}, spoken("a", "b", "c", "d", "e", "f"), spoken(strings.Repeat("x", 2001)), spoken(" ")}
	over := 1.5
	bad = append(bad, &task.Voice{Alternatives: []task.VoiceAlt{{Text: "a", Confidence: &over}}})
	for i, v := range bad {
		if _, _, err := h.eng.Submit(context.Background(), CreateInput{Objective: "a", Voice: v}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
	if !sameWords("Halo, Tuan!", "halo tuan") || sameWords("jam 3", "jam 4") {
		t.Fatal("sameWords")
	}
}
