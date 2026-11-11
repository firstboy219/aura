package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

type fakePictures struct {
	mu    sync.Mutex
	asked []string
}

func (f *fakePictures) Find(_ context.Context, name string) (*tools.Picture, error) {
	f.mu.Lock()
	f.asked = append(f.asked, name)
	f.mu.Unlock()
	switch name {
	case "Nvidia":
		return nil, nil // the source has no picture
	case "Xi Jinping":
		return nil, errors.New("source down")
	}
	return &tools.Picture{ID: strings.Repeat("ab", 32), ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\n"), Title: name, SourceURL: "https://en.wikipedia.org/wiki/X"}, nil
}

func workspaceReply() string {
	return replyJSON("Sudah saya catat, Tuan.",
		nullBlock("figures", map[string]any{"items": []any{nullItem("Donald Trump", "tarif baru", ""), nullItem("Nvidia", "skandal", ""), nullItem("Xi Jinping", "aliansi", "https://evil.example/x.png")}}),
		nullBlock("note", map[string]any{"items": []any{nullItem("beli susu besok", "", ""), nullItem("telepon Pak Budi", "", "")}}),
		nullBlock("player", map[string]any{"text": "jazz"}))
}

// ADR-0061: a reply's note block is kept by the Core exactly once, figures get
// a picture only from the Core's own lookup (never a model's URL), and a
// missing or failed lookup just leaves the figure without one.
func TestReplyKeepsNotesAndIllustratesFigures(t *testing.T) {
	pics := &fakePictures{}
	h := newHarness(t, harnessOpts{replies: true, pictures: pics, respond: replyModel(func(string) string { return noSteps },
		func(string) gatewaytest.Reply { return gatewaytest.Reply{Content: workspaceReply()} })})
	ctx := context.Background()
	tk, _, err := h.eng.Submit(ctx, CreateInput{Objective: "Analisa pasar lalu catat beli susu besok dan telepon Pak Budi", AllowDomains: []string{h.host}})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	r, _ := h.eng.GetReply(ctx, tk.ID)
	if r == nil || len(r.View) != 3 {
		t.Fatalf("reply: %+v", r)
	}
	f := r.View[0].Items
	if f[0].Image != strings.Repeat("ab", 32) || f[0].URL != "https://en.wikipedia.org/wiki/X" || f[1].Image != "" || f[2].Image != "" || f[2].URL != "" {
		t.Fatalf("figures: %+v", f)
	}
	if m, err := h.eng.GetMedia(ctx, f[0].Image); err != nil || m.ContentType != "image/png" {
		t.Fatalf("media: %v %+v", err, m)
	}
	if _, err := h.eng.GetMedia(ctx, "nope"); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("unknown media: %v", err)
	}
	ns, _ := h.eng.Notes(ctx, 0)
	if len(ns) != 2 || ns[0].Text != "telepon Pak Budi" || ns[1].Text != "beli susu besok" || ns[0].TaskID != tk.ID {
		t.Fatalf("notes: %+v", ns)
	}
	// Storing the reply again (recovery) keeps nothing twice.
	if err := h.eng.storeReply(ctx, tk, r.Text, r.View, r.Scene, r.Basis, nil, nil); err != nil {
		t.Fatal(err)
	}
	if ns, _ := h.eng.Notes(ctx, 0); len(ns) != 2 {
		t.Fatalf("notes after a second store: %d", len(ns))
	}
	// A name already resolved is not looked up again; a failed lookup is.
	before := len(pics.asked)
	if id, _ := h.eng.picture(ctx, "  donald   TRUMP "); id == "" {
		t.Fatal("cached picture not found")
	}
	h.eng.picture(ctx, "Nvidia")
	h.eng.picture(ctx, "Xi Jinping")
	if got := pics.asked[before:]; len(got) != 1 || got[0] != "Xi Jinping" {
		t.Fatalf("lookups after cache: %v", got)
	}
}

// Without the web.search grant no name leaves the Core.
func TestFiguresAreNotLookedUpWithoutWebGrant(t *testing.T) {
	pics := &fakePictures{}
	h := newHarness(t, harnessOpts{replies: true, pictures: pics, respond: replyModel(func(string) string { return noSteps },
		func(string) gatewaytest.Reply { return gatewaytest.Reply{Content: workspaceReply()} })})
	ctx := context.Background()
	tk, _, err := h.eng.Submit(ctx, CreateInput{Objective: "Jelaskan siapa saja yang terlibat", Tools: []string{"calc"}, AllowDomains: []string{h.host}})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Succeeded)
	r, _ := h.eng.GetReply(ctx, tk.ID)
	if len(pics.asked) != 0 || r == nil || r.View[0].Items[0].Image != "" {
		t.Fatalf("lookups without grant: %v", pics.asked)
	}
}

func TestOwnerNotes(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	ctx := context.Background()
	if _, err := h.eng.AddNote(ctx, "  \n "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty note: %v", err)
	}
	n, err := h.eng.AddNote(ctx, "bayar listrik\x00")
	if err != nil || n.Text != "bayar listrik" {
		t.Fatalf("add: %v %+v", err, n)
	}
	done, text := true, "bayar listrik & air"
	if u, err := h.eng.UpdateNote(ctx, n.ID, &text, &done); err != nil || !u.Done || u.Text != text {
		t.Fatalf("update: %v %+v", err, u)
	}
	if _, err := h.eng.UpdateNote(ctx, "missing", nil, &done); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := h.eng.DeleteNote(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if ns, _ := h.eng.Notes(ctx, 0); len(ns) != 0 {
		t.Fatalf("after delete: %+v", ns)
	}
}
