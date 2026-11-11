package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/browser"
	"aura.local/core/internal/config"
	"aura.local/core/internal/egress"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/vault"
)

// fakeHost speaks the browser host protocol: a search page (e1 = query box,
// e2 = Search button) whose click leads to a results page.
type fakeHost struct {
	mu        sync.Mutex
	sessions  map[string]string // session -> url
	calls     []string
	buttonFor string // name of e2 (changed to simulate a page that changed)
	blocked   bool
	status    int // page status on goto (0 = 200)
	typed     map[string]string
}

func startFakeHost(t *testing.T) (*fakeHost, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	h := &fakeHost{sessions: map[string]string{}, buttonFor: "Search", typed: map[string]string{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(c)
		}
	}()
	return h, sock
}

func (h *fakeHost) serve(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     string         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		json.Unmarshal(line, &m)
		res, errObj := h.handle(m.Method, m.Params)
		out := map[string]any{"id": m.ID}
		if errObj != nil {
			out["error"] = errObj
		} else {
			out["result"] = res
		}
		b, _ := json.Marshal(out)
		c.Write(append(b, '\n'))
	}
}

func (h *fakeHost) handle(method string, p map[string]any) (any, any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, method)
	sid, _ := p["session_id"].(string)
	switch method {
	case "session.open":
		h.sessions[sid] = "about:blank"
		return map[string]any{"session_id": sid}, nil
	case "session.close":
		delete(h.sessions, sid)
		return map[string]any{"closed": true}, nil
	case "session.close_all":
		h.sessions = map[string]string{}
		return map[string]any{}, nil
	}
	if _, ok := h.sessions[sid]; !ok {
		return nil, map[string]any{"code": "no_session", "message": "unknown session"}
	}
	switch method {
	case "page.goto":
		h.sessions[sid] = p["url"].(string)
		st := 200
		if h.status != 0 {
			st = h.status
		}
		return map[string]any{"url": p["url"], "title": "Search", "status": st, "site_blocked": h.blocked}, nil
	case "page.inspect":
		u := h.sessions[sid]
		if strings.HasSuffix(u, "/results") {
			return map[string]any{"url": u, "title": "Results", "text": "3 results for aura", "elements": []any{}}, nil
		}
		return map[string]any{"url": u, "title": "Search", "text": "Search the catalogue",
			"elements": []any{map[string]any{"ref": "e1", "tag": "input", "name": "q", "type": "text"},
				map[string]any{"ref": "e2", "tag": "button", "name": h.buttonFor}}}, nil
	case "page.describe":
		return map[string]any{"url": h.sessions[sid], "tag": "button", "name": h.buttonFor}, nil
	case "page.type":
		h.typed[sid] = p["text"].(string)
		return map[string]any{"typed_chars": len(p["text"].(string))}, nil
	case "page.click":
		h.sessions[sid] = strings.TrimSuffix(h.sessions[sid], "/") + "/results"
		return map[string]any{"url": h.sessions[sid], "title": "Results", "status": 200}, nil
	case "page.screenshot":
		return map[string]any{"url": h.sessions[sid], "png_base64": "iVBORw0KGgo="}, nil
	case "page.mouse_click", "page.key", "page.wait", "page.scroll", "page.back":
		return map[string]any{"url": h.sessions[sid]}, nil
	case "page.keyboard_type":
		h.typed[sid] = p["text"].(string)
		return map[string]any{"url": h.sessions[sid]}, nil
	case "session.export":
		return map[string]any{"storage_state": map[string]any{"cookies": []any{
			map[string]any{"name": "sid", "value": "handoff-cookie", "domain": "fixture.test", "path": "/", "expires": -1}}, "origins": []any{}}}, nil
	}
	return nil, map[string]any{"code": "unknown_method", "message": method}
}

func browserModel(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
	switch {
	case isSys(req, "replanning module"):
		return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":null}`} // no second attempt: fail visibly
	case isSys(req, "planning module"):
		return planWith([]string{"reports/result.md exists"},
			gatewaytest.Step(1, "open the catalogue", "browser.open", map[string]any{"url": "http://fixture.test/search"}),
			gatewaytest.Step(2, "read the search page", "browser.inspect", nil),
			gatewaytest.Step(3, "type the query into the search box", "browser.type", map[string]any{"input": `{"text": "aura"}`}),
			gatewaytest.Step(4, "click the Search button", "browser.click", nil),
			gatewaytest.Step(5, "read the results", "browser.inspect", nil),
			gatewaytest.Step(6, "write the result", "workspace.write", map[string]any{"path": "reports/result.md"}))
	case isSys(req, "which element"):
		if strings.Contains(req.Messages[len(req.Messages)-1].Content, "type the query") {
			return gatewaytest.Reply{Content: `{"ref":"e1"}`}
		}
		return gatewaytest.Reply{Content: `{"ref":"e2"}`}
	}
	return goodModel(r, req)
}

var browserTools = []string{"browser.open", "browser.inspect", "browser.type", "browser.click", "workspace.write"}

func browserHarness(t *testing.T) (*harness, *fakeHost, *egress.Proxy) {
	h := newHarness(t, harnessOpts{respond: browserModel})
	fh, sock := startFakeHost(t)
	px := egress.New(tools.NewTestGuard(nil), nil)
	h.eng.SetBrowser(browser.New(sock), px)
	return h, fh, px
}

// RS-8 / BRW-01, BRW-05: open, inspect, type (value from the plan), the click
// waits for an A2 approval bound to page + element + typed values, then runs;
// the session is closed when the task ends.
func TestGovernedBrowsing(t *testing.T) {
	h, fh, _ := browserHarness(t)
	ctx := context.Background()
	tk := h.submitTools("Search the catalogue at http://fixture.test/search for aura and write reports/result.md", browserTools...)
	h.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(ctx, h.st.R)
	if len(aps) != 1 || aps[0].ToolID != "browser.click" {
		t.Fatalf("approvals %+v", aps)
	}
	var params map[string]any
	b, _ := json.Marshal(aps[0].Parameters)
	json.Unmarshal(b, &params)
	if params["url"] != "http://fixture.test/search" || !strings.Contains(string(b), `"aura"`) || !strings.Contains(string(b), "Search") {
		t.Fatalf("approval does not show page, element and typed value: %s", b)
	}
	if _, err := h.eng.Approve(ctx, aps[0].ID, aps[0].ActionHash, "owner"); err != nil {
		t.Fatal(err)
	}
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s %s", done.State, deref(done.Reason), errorDetail(h.events(tk.ID)))
	}
	open := func() int { fh.mu.Lock(); defer fh.mu.Unlock(); return len(fh.sessions) }
	for i := 0; i < 50 && open() != 0; i++ {
		time.Sleep(40 * time.Millisecond) // closed right after the final state is written
	}
	if open() != 0 {
		t.Fatal("browser session not closed when the task ended")
	}
	for _, ev := range h.events(tk.ID) {
		if strings.Contains(string(ev.Data), `"text":"aura"`) && ev.Type != "PLAN_CREATED" {
			t.Fatalf("typed value traced in %s: %s", ev.Type, ev.Data)
		}
	}
}

// BRW-05: if the element changed after approval, the approved click is refused.
func TestApprovedClickRefusedWhenPageChanged(t *testing.T) {
	h, fh, _ := browserHarness(t)
	ctx := context.Background()
	tk := h.submitTools("Search the catalogue at http://fixture.test/search for aura and write reports/result.md", browserTools...)
	h.wait(tk.ID, task.WaitingApproval)
	fh.mu.Lock()
	fh.buttonFor = "Delete my account" // the page changed under the approval
	fh.mu.Unlock()
	aps, _ := policy.Pending(ctx, h.st.R)
	h.eng.Approve(ctx, aps[0].ID, aps[0].ActionHash, "owner")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	fh.mu.Lock()
	clicked := strings.Contains(strings.Join(fh.calls, ","), "page.click")
	fh.mu.Unlock()
	if done.State == task.Succeeded && clicked {
		t.Fatal("approved click ran on a changed page")
	}
	if clicked {
		t.Fatal("click executed despite the mismatch")
	}
}

// BRW-08: a bot wall stops the task with SITE_BLOCKED and no retry.
func TestSiteBlocked(t *testing.T) {
	h, fh, _ := browserHarness(t)
	fh.blocked = true
	tk := h.submitTools("Search the catalogue at http://fixture.test/search for aura and write reports/result.md", browserTools...)
	done := h.wait(tk.ID, task.Failed, task.Succeeded)
	evs := h.events(tk.ID)
	if done.State != task.Failed || deref(done.ReasonCode) != "SITE_BLOCKED" || done.Replans != 0 {
		t.Fatalf("%s %s %s", done.State, deref(done.ReasonCode), errorDetail(evs))
	}
	fh.mu.Lock()
	gotos := strings.Count(strings.Join(fh.calls, ","), "page.goto")
	fh.mu.Unlock()
	if gotos > 2 {
		t.Fatalf("blocked site retried %d times", gotos)
	}
}

// The browser can only act on elements the Core saw, with values from the plan.
func TestBrowserArgumentBinding(t *testing.T) {
	h, _, _ := browserHarness(t)
	s := &session{t: &task.Task{ID: "T", Scope: task.Scope{AllowDomains: []string{"fixture.test"}}}}
	in := `{"text":"aura"}`
	s.plan = &plannedStep{Input: &in}
	call := func(tool, args string) string {
		_, _, why := h.eng.validateBrowserArgs(s, toolCall{ToolID: tool, Arguments: json.RawMessage(args)})
		return why
	}
	if why := call("browser.type", `{"ref":"e1","text":"aura"}`); why != "stale_ref" {
		t.Fatalf("ref before any inspect: %q", why)
	}
	if why := call("browser.type", `{"ref":"e1","text":"something else"}`); why != "arguments_differ_from_plan" {
		t.Fatalf("value not from the plan: %q", why)
	}
	u := "http://evil.test/"
	s.plan = &plannedStep{URL: &u}
	if why := call("browser.open", `{"url":"http://evil.test/"}`); why != "domain_not_allowed" {
		t.Fatalf("open outside the allowlist: %q", why)
	}
}

// An error page (here the egress proxy's 502 refusal) is never treated as content.
func TestBrowserErrorPageIsAFailure(t *testing.T) {
	h, fh, _ := browserHarness(t)
	fh.status = 502
	tk := h.submitTools("Search the catalogue at http://fixture.test/search for aura and write reports/result.md", browserTools...)
	done := h.wait(tk.ID, task.Failed, task.Succeeded, task.WaitingApproval)
	if done.State != task.Failed || !strings.Contains(errorDetail(h.events(tk.ID))+eventTypes(h.events(tk.ID)), "TOOL_FAILED") {
		t.Fatalf("%s %s", done.State, errorDetail(h.events(tk.ID)))
	}
}

// The kill switch closes every browser session and removes its egress credentials.
func TestKillSwitchClosesBrowserSessions(t *testing.T) {
	h, fh, px := browserHarness(t)
	tk := h.submitTools("Search the catalogue at http://fixture.test/search for aura and write reports/result.md", browserTools...)
	h.wait(tk.ID, task.WaitingApproval)
	fh.mu.Lock()
	open := len(fh.sessions)
	fh.mu.Unlock()
	if open != 1 {
		t.Fatalf("expected one open session while waiting for approval, got %d", open)
	}
	if _, err := h.eng.Stop(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if len(fh.sessions) != 0 {
		t.Fatal("kill switch left a browser session open")
	}
	_ = px
}

// D.1 P5 workspace handoff: the owner takes over (the task pauses and AURA's
// browser tools are refused), acts on the page (typed text never recorded),
// returns control (refs invalidated) and the task resumes.
func TestBrowserTakeoverAndReturn(t *testing.T) {
	h, fh, _ := browserHarness(t)
	ctx := context.Background()
	tk := h.submitTools("Search the catalogue at http://fixture.test/search for aura and write reports/result.md", browserTools...)
	h.wait(tk.ID, task.WaitingApproval)
	if err := h.eng.BrowserTakeover(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	if cur, _ := task.Get(ctx, h.st.R, tk.ID); cur.State != task.Paused {
		t.Fatalf("task not paused during takeover: %s", cur.State)
	}
	s := &session{t: &task.Task{ID: tk.ID, Scope: task.Scope{AllowDomains: []string{"fixture.test"}}}}
	if _, _, why := h.eng.validateBrowserArgs(s, toolCall{ToolID: "browser.inspect", Arguments: json.RawMessage(`{}`)}); why != "owner_has_control" {
		t.Fatalf("AURA acted during takeover: %q", why)
	}
	if _, err := h.eng.BrowserInput(ctx, tk.ID, OwnerInput{Action: "type", Text: "owner-secret-text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.BrowserInput(ctx, tk.ID, OwnerInput{Action: "goto", URL: "https://evil.test/"}); err == nil {
		t.Fatal("owner navigation outside the session's domains accepted")
	}
	if png, _, err := h.eng.BrowserScreen(ctx, tk.ID); err != nil || len(png) == 0 {
		t.Fatalf("screen: %v", err)
	}
	if hits := dbContains(t, h, "owner-secret-text"); len(hits) > 0 {
		t.Fatalf("owner-typed text recorded in %v", hits)
	}
	if err := h.eng.BrowserReturn(ctx, tk.ID); err != nil {
		t.Fatal(err)
	}
	bs := h.eng.existingBrowserSession(tk.ID)
	if bs == nil || bs.ownerControl || len(bs.elements) != 0 {
		t.Fatal("control not returned or refs not invalidated")
	}
	if cur, _ := task.Get(ctx, h.st.R, tk.ID); cur.State == task.Paused {
		t.Fatal("task still paused after control was returned")
	}
	if err := h.eng.BrowserReturn(ctx, tk.ID); err == nil {
		t.Fatal("return accepted without a takeover")
	}
	_ = fh
}

// The owner signs in to a login profile by hand; the session is saved encrypted.
func TestOwnerProfileLogin(t *testing.T) {
	h, _, _ := browserHarness(t)
	ctx := context.Background()
	h.eng.cfg.Browser.Logins = []config.LoginProfile{{Name: "shop", Domains: []string{"fixture.test"}, Username: "u", PasswordRef: "vault:p"}}
	v, _ := vault.New(bytes.Repeat([]byte{5}, 32))
	h.eng.vault = v
	if _, err := h.eng.StartProfileLogin(ctx, "shop", "https://other.test/login", nil); err == nil {
		t.Fatal("start URL outside the profile's domains accepted")
	}
	key, err := h.eng.StartProfileLogin(ctx, "shop", "https://fixture.test/login", []string{"accounts.example-idp.test"})
	if err != nil || key != "owner-shop" {
		t.Fatalf("%q %v", key, err)
	}
	if _, err := h.eng.BrowserInput(ctx, key, OwnerInput{Action: "click", X: 100, Y: 200}); err != nil {
		t.Fatal(err)
	}
	saved, err := h.eng.FinishProfileLogin(ctx, "shop", true)
	if err != nil || !saved {
		t.Fatalf("saved=%v %v", saved, err)
	}
	st, _ := json.Marshal(h.eng.savedState(ctx, "shop"))
	if !strings.Contains(string(st), "handoff-cookie") {
		t.Fatalf("session not saved: %s", st)
	}
	if h.eng.existingBrowserSession(key) != nil {
		t.Fatal("owner session not closed")
	}
}

// A watch whose source needs JavaScript is read in Chromium (ADR-0062): a short session limited to the watch's
// domains, closed afterwards; the page text is the watched content.
func TestRenderPageForBackgroundWatch(t *testing.T) {
	h, fh, px := browserHarness(t)
	text, err := h.eng.renderPage(context.Background(), "http://fixture.test/results", []string{"fixture.test"})
	if err != nil || text != "3 results for aura" {
		t.Fatalf("%q %v", text, err)
	}
	fh.mu.Lock()
	open := len(fh.sessions)
	calls := strings.Join(fh.calls, ",")
	fh.mu.Unlock()
	if open != 0 || calls != "session.close_all,session.open,page.goto,page.wait,page.inspect,session.close" {
		t.Fatalf("sessions left %d, calls %s", open, calls)
	}
	_ = px
	if _, err := h.eng.renderPage(context.Background(), "http://elsewhere.test/", []string{"fixture.test"}); err == nil {
		t.Fatal("a page outside the watch's domains was rendered")
	}
	if _, err := h.eng.CreateTrigger(context.Background(), &Trigger{Name: "promo", Kind: "watch", IntervalSeconds: ptr64(3600),
		Spec: TriggerSpec{Sources: []string{"http://fixture.test/results"}, AllowDomains: []string{"fixture.test"}, Render: "javascript"}}); err == nil {
		t.Fatal("unknown render accepted")
	}
	tr, err := h.eng.CreateTrigger(context.Background(), &Trigger{Name: "promo", Kind: "watch", IntervalSeconds: ptr64(3600),
		Spec: TriggerSpec{Sources: []string{"http://fixture.test/results"}, AllowDomains: []string{"fixture.test"}, Render: "browser"}})
	if err != nil || tr.Spec.Render != "browser" {
		t.Fatalf("%v %+v", err, tr)
	}
}

func ptr64(v int64) *int64 { return &v }
