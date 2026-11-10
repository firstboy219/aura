package engine

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
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

const (
	testPassword = "s3cret-Pass-9f2c"
	testCode     = "481516"
)

// authHost is a fake browser host with a login page, a one-time-code page,
// a download page and an upload page.
type authHost struct {
	mu       sync.Mutex
	url      map[string]string
	typed    []string
	uploads  []string
	download []byte
	dlName   string
	opened   []map[string]any
	passType string
}

func (h *authHost) elements(u string) []any {
	switch {
	case strings.HasSuffix(u, "/login"):
		return []any{map[string]any{"ref": "e1", "tag": "input", "name": "Email", "type": "email"},
			map[string]any{"ref": "e2", "tag": "input", "name": "Password", "type": h.passType},
			map[string]any{"ref": "e3", "tag": "button", "name": "Sign in"}}
	case strings.HasSuffix(u, "/code"):
		return []any{map[string]any{"ref": "e1", "tag": "input", "name": "One-time code", "type": "text"},
			map[string]any{"ref": "e2", "tag": "button", "name": "Verify"}}
	case strings.HasSuffix(u, "/files"):
		return []any{map[string]any{"ref": "e1", "tag": "a", "name": "Download report", "href": "/report"}}
	case strings.HasSuffix(u, "/upload"):
		return []any{map[string]any{"ref": "e1", "tag": "input", "name": "Attachment", "type": "file"},
			map[string]any{"ref": "e2", "tag": "button", "name": "Send"}}
	}
	return []any{}
}

func (h *authHost) handle(method string, p map[string]any) (any, any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sid, _ := p["session_id"].(string)
	switch method {
	case "session.open":
		h.url[sid] = "about:blank"
		h.opened = append(h.opened, p)
		return map[string]any{}, nil
	case "session.close", "session.close_all":
		delete(h.url, sid)
		return map[string]any{}, nil
	case "session.export":
		return map[string]any{"storage_state": map[string]any{"cookies": []any{
			map[string]any{"name": "sid", "value": "cookie-value", "domain": "fixture.test", "path": "/", "expires": -1},
			map[string]any{"name": "tracker", "value": "x", "domain": "ads.other.test", "path": "/", "expires": -1}}, "origins": []any{}}}, nil
	case "page.goto":
		h.url[sid] = p["url"].(string)
		return map[string]any{"url": p["url"], "title": "Page", "status": 200}, nil
	case "page.inspect":
		return map[string]any{"url": h.url[sid], "title": "Page", "text": "page text", "elements": h.elements(h.url[sid])}, nil
	case "page.describe":
		for _, el := range h.elements(h.url[sid]) {
			m := el.(map[string]any)
			if m["ref"] == p["ref"] {
				return map[string]any{"url": h.url[sid], "tag": m["tag"], "name": m["name"], "type": m["type"]}, nil
			}
		}
		return nil, map[string]any{"code": "stale_ref", "message": "no such element"}
	case "page.type":
		h.typed = append(h.typed, p["text"].(string))
		return map[string]any{}, nil
	case "page.click":
		u := h.url[sid]
		switch {
		case strings.HasSuffix(u, "/login"):
			h.url[sid] = strings.TrimSuffix(u, "/login") + "/code"
		case strings.HasSuffix(u, "/code"):
			h.url[sid] = strings.TrimSuffix(u, "/code") + "/account"
		}
		return map[string]any{"url": h.url[sid], "title": "Next", "status": 200}, nil
	case "page.download":
		return map[string]any{"url": "https://fixture.test/report", "suggested_filename": h.dlName,
			"content_base64": base64.StdEncoding.EncodeToString(h.download)}, nil
	case "page.upload":
		b, _ := base64.StdEncoding.DecodeString(p["content_base64"].(string))
		h.uploads = append(h.uploads, string(b))
		return map[string]any{"uploaded_bytes": len(b)}, nil
	}
	return nil, map[string]any{"code": "unknown_method", "message": method}
}

func startAuthHost(t *testing.T) (*authHost, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	h := &authHost{url: map[string]string{}, passType: "password", dlName: "report.csv", download: []byte("month,total\njan,10\n")}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
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
					res, e := h.handle(m.Method, m.Params)
					out := map[string]any{"id": m.ID}
					if e != nil {
						out["error"] = e
					} else {
						out["result"] = res
					}
					b, _ := json.Marshal(out)
					c.Write(append(b, '\n'))
				}
			}(c)
		}
	}()
	return h, sock
}

func authModel(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
	user := req.Messages[len(req.Messages)-1].Content
	switch {
	case isSys(req, "replanning module"):
		return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":null}`}
	case isSys(req, "planning module"):
		obj := req.Messages[len(req.Messages)-1].Content
		switch {
		case strings.Contains(obj, "sign in"):
			return planWith([]string{"reports/account.md exists"},
				gatewaytest.Step(1, "open the login page", "browser.open", map[string]any{"url": "https://fixture.test/login"}),
				gatewaytest.Step(2, "read the login page", "browser.inspect", nil),
				gatewaytest.Step(3, "sign in", "browser.login", map[string]any{"input": `{"profile": "shop"}`}),
				gatewaytest.Step(4, "read the code page", "browser.inspect", nil),
				gatewaytest.Step(5, "enter the one-time code", "browser.mfa", nil),
				gatewaytest.Step(6, "write the report", "workspace.write", map[string]any{"path": "reports/account.md"}))
		case strings.Contains(obj, "download"):
			return planWith([]string{"reports/dl.md exists"},
				gatewaytest.Step(1, "open the files page", "browser.open", map[string]any{"url": "https://fixture.test/files"}),
				gatewaytest.Step(2, "read it", "browser.inspect", nil),
				gatewaytest.Step(3, "download the report", "browser.download", nil),
				gatewaytest.Step(4, "write the report", "workspace.write", map[string]any{"path": "reports/dl.md"}))
		default: // upload
			return planWith([]string{"reports/up.md exists"},
				gatewaytest.Step(1, "open the upload page", "browser.open", map[string]any{"url": "https://fixture.test/upload"}),
				gatewaytest.Step(2, "read it", "browser.inspect", nil),
				gatewaytest.Step(3, "attach the invoice", "browser.upload", map[string]any{"path": "data/invoice.txt"}),
				gatewaytest.Step(4, "write the report", "workspace.write", map[string]any{"path": "reports/up.md"}))
		}
	case isSys(req, "fields of an inspected login page"):
		return gatewaytest.Reply{Content: `{"user_ref":"e1","pass_ref":"e2","submit_ref":"e3"}`}
	case isSys(req, "one-time code field"):
		return gatewaytest.Reply{Content: `{"ref":"e1","submit_ref":"e2"}`}
	case isSys(req, "which element"):
		_ = user
		return gatewaytest.Reply{Content: `{"ref":"e1"}`}
	}
	return goodModel(r, req)
}

func authHarness(t *testing.T) (*harness, *authHost) {
	h := newHarness(t, harnessOpts{respond: authModel})
	fh, sock := startAuthHost(t)
	h.eng.SetBrowser(browser.New(sock), egress.New(tools.NewTestGuard(nil), nil))
	h.eng.cfg.Browser.Logins = []config.LoginProfile{{Name: "shop", Domains: []string{"fixture.test"}, Username: "owner@example.com", PasswordRef: "vault:shop-password"}}
	h.eng.secret = func(_ context.Context, ref string) (string, error) {
		if ref == "vault:shop-password" {
			return testPassword, nil
		}
		return "", sql.ErrNoRows
	}
	v, _ := vault.New(bytes.Repeat([]byte{3}, 32))
	h.eng.vault = v
	return h, fh
}

// dbContains scans every text and blob column of every table.
func dbContains(t *testing.T, h *harness, needle string) []string {
	t.Helper()
	var hits []string
	rows, _ := h.st.R.Query("select name from sqlite_master where type='table'")
	var tables []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		r, err := h.st.R.Query("select * from " + tb)
		if err != nil {
			continue
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			r.Scan(ptrs...)
			for _, v := range vals {
				var s string
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				}
				if strings.Contains(s, needle) {
					hits = append(hits, tb)
				}
			}
		}
		r.Close()
	}
	return hits
}

// ADR-0040: login with an owner profile (A2), one-time code through the Core
// only, session saved encrypted; neither the password nor the code is ever stored.
func TestBrowserLoginWithProfileAndOneTimeCode(t *testing.T) {
	h, fh := authHarness(t)
	ctx := context.Background()
	tools := []string{"browser.open", "browser.inspect", "browser.login", "browser.mfa", "workspace.write"}
	tk, _, err := h.eng.Submit(ctx, CreateInput{Objective: "sign in to https://fixture.test/login and write reports/account.md",
		Tools: tools, AllowDomains: []string{"fixture.test"}, BrowserProfile: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.WaitingApproval)
	aps, _ := policy.Pending(ctx, h.st.R)
	if len(aps) != 1 || aps[0].ToolID != "browser.login" {
		t.Fatalf("approvals %+v", aps)
	}
	if _, err := h.eng.Approve(ctx, aps[0].ID, aps[0].ActionHash, "owner"); err != nil {
		t.Fatal(err)
	}
	h.wait(tk.ID, task.Waiting) // the one-time code
	if err := h.eng.BrowserCode(ctx, tk.ID, testCode); err != nil {
		t.Fatal(err)
	}
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s", done.State, errorDetail(h.events(tk.ID)))
	}
	fh.mu.Lock()
	typed := strings.Join(fh.typed, "|")
	fh.mu.Unlock()
	if !strings.Contains(typed, "owner@example.com") || !strings.Contains(typed, testPassword) || !strings.Contains(typed, testCode) {
		t.Fatalf("the Core did not type the credentials and code: %q", typed)
	}
	if hits := dbContains(t, h, "sign in to https://fixture.test/login"); len(hits) == 0 {
		t.Fatal("the database scan finds nothing: the secret checks below would be meaningless")
	}
	for _, secret := range []string{testPassword, testCode} {
		if hits := dbContains(t, h, secret); len(hits) > 0 {
			t.Fatalf("%q stored in %v", secret, hits)
		}
	}
	for _, c := range h.fake.Calls {
		for _, m := range c.Messages {
			if strings.Contains(m.Content, testPassword) || strings.Contains(m.Content, testCode) {
				t.Fatal("a secret reached a model prompt")
			}
		}
	}
	// The session was saved encrypted, limited to the profile's domains, and is loaded next time.
	st := h.eng.savedState(ctx, "shop")
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), "cookie-value") || strings.Contains(string(b), "ads.other.test") {
		t.Fatalf("saved session: %s", b)
	}
	if hits := dbContains(t, h, "cookie-value"); len(hits) > 0 {
		t.Fatalf("session stored in clear in %v", hits)
	}
}

func TestBrowserLoginGates(t *testing.T) {
	h, fh := authHarness(t)
	ctx := context.Background()
	tools := []string{"browser.open", "browser.inspect", "browser.login", "browser.mfa", "workspace.write"}
	// No profile named at submission: refused, no approval asked.
	tk, _, _ := h.eng.Submit(ctx, CreateInput{Objective: "sign in to https://fixture.test/login and write reports/account.md",
		Tools: tools, AllowDomains: []string{"fixture.test"}})
	if done := h.wait(tk.ID, task.Failed, task.Succeeded, task.WaitingApproval); done.State != task.Failed {
		t.Fatalf("login without a granted profile: %s", done.State)
	}
	if !strings.Contains(strings.Join(deniedReasons(t, h, tk.ID), ","), "login_profile_not_granted") {
		t.Fatalf("denials %v", deniedReasons(t, h, tk.ID))
	}
	// The "password" field is a plain text input: refused (the password would be readable on the page).
	fh.mu.Lock()
	fh.passType = "text"
	fh.mu.Unlock()
	tk2, _, _ := h.eng.Submit(ctx, CreateInput{Objective: "sign in to https://fixture.test/login and write reports/account.md",
		Tools: tools, AllowDomains: []string{"fixture.test"}, BrowserProfile: "shop"})
	if done := h.wait(tk2.ID, task.Failed, task.Succeeded, task.WaitingApproval); done.State != task.Failed {
		t.Fatalf("password into a text field: %s", done.State)
	}
	if _, _, err := h.eng.Submit(ctx, CreateInput{Objective: "x", BrowserProfile: "nope"}); err == nil {
		t.Fatal("unknown profile accepted")
	}
	if err := h.eng.BrowserCode(ctx, tk2.ID, "123"); err == nil {
		t.Fatal("a code was accepted for a task that is not waiting for one")
	}
}

func TestBrowserDownloadQuarantine(t *testing.T) {
	h, fh := authHarness(t)
	ctx := context.Background()
	tools := []string{"browser.open", "browser.inspect", "browser.download", "workspace.write"}
	run := func() *task.Task {
		tk, _, _ := h.eng.Submit(ctx, CreateInput{Objective: "download the report from https://fixture.test/files and write reports/dl.md",
			Tools: tools, AllowDomains: []string{"fixture.test"}})
		return h.wait(tk.ID, task.Succeeded, task.Failed)
	}
	ok := run()
	if ok.State != task.Succeeded {
		t.Fatalf("csv download: %s %s", ok.State, errorDetail(h.events(ok.ID)))
	}
	if f, err := h.ws.Read(ok.ID, "downloads/report.csv"); err != nil || !strings.Contains(f.Content, "jan,10") {
		t.Fatalf("downloaded file not in the workspace: %v", err)
	}
	if !fh.opened[len(fh.opened)-1]["downloads"].(bool) {
		t.Fatal("downloads not enabled for a task granted browser.download")
	}
	for name, c := range map[string]struct {
		file string
		data []byte
	}{"executable named .pdf": {"report.pdf", []byte("MZ\x90\x00binary")}, "zip archive": {"report.zip", []byte("PK\x03\x04zip")},
		"script": {"run.sh", []byte("#!/bin/sh\nrm -rf /")}} {
		fh.mu.Lock()
		fh.dlName, fh.download = c.file, c.data
		fh.mu.Unlock()
		bad := run()
		if bad.State != task.Failed {
			t.Fatalf("%s accepted", name)
		}
		if _, err := h.ws.Read(bad.ID, "downloads/"+c.file); err == nil {
			t.Fatalf("%s reached the workspace", name)
		}
	}
}

func TestBrowserUploadPolicy(t *testing.T) {
	h, fh := authHarness(t)
	ctx := context.Background()
	tools := []string{"browser.open", "browser.inspect", "browser.upload", "workspace.write"}
	submit := func() *task.Task {
		tk, _, err := h.eng.Submit(ctx, CreateInput{Objective: "upload data/invoice.txt at https://fixture.test/upload and write reports/up.md",
			Tools: tools, AllowDomains: []string{"fixture.test"}, Attachments: map[string]string{"data/invoice.txt": "invoice 42"}})
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}
	// Off by default.
	tk := submit()
	if done := h.wait(tk.ID, task.Failed, task.Succeeded, task.WaitingApproval); done.State != task.Failed ||
		!strings.Contains(strings.Join(deniedReasons(t, h, tk.ID), ","), "uploads_disabled") {
		t.Fatalf("upload with uploads disabled: %s %v", done.State, deniedReasons(t, h, tk.ID))
	}
	// Enabled: needs an approval bound to the file hash; a changed file voids it.
	h.eng.cfg.Browser.UploadsEnabled = true
	tk2 := submit()
	h.wait(tk2.ID, task.WaitingApproval)
	aps, _ := policy.Pending(ctx, h.st.R)
	if len(aps) != 1 || aps[0].ToolID != "browser.upload" {
		t.Fatalf("approvals %+v", aps)
	}
	b, _ := json.Marshal(aps[0].Parameters)
	if !strings.Contains(string(b), "sha256") {
		t.Fatalf("approval does not bind the file: %s", b)
	}
	h.ws.Write(tk2.ID, "data/invoice.txt", "invoice 42 — changed")
	h.eng.Approve(ctx, aps[0].ID, aps[0].ActionHash, "owner")
	done := h.wait(tk2.ID, task.Failed, task.Succeeded)
	fh.mu.Lock()
	n := len(fh.uploads)
	fh.mu.Unlock()
	if done.State == task.Succeeded || n != 0 {
		t.Fatalf("changed file uploaded after approval: %s uploads=%d", done.State, n)
	}
	// Unchanged file: uploaded once.
	tk3 := submit()
	h.wait(tk3.ID, task.WaitingApproval)
	aps, _ = policy.Pending(ctx, h.st.R)
	h.eng.Approve(ctx, aps[0].ID, aps[0].ActionHash, "owner")
	if done := h.wait(tk3.ID, task.Succeeded, task.Failed); done.State != task.Succeeded {
		t.Fatalf("upload: %s %s", done.State, errorDetail(h.events(tk3.ID)))
	}
	fh.mu.Lock()
	defer fh.mu.Unlock()
	if len(fh.uploads) != 1 || fh.uploads[0] != "invoice 42" {
		t.Fatalf("uploads %q", fh.uploads)
	}
}

func TestBrowserSessionImportFilters(t *testing.T) {
	h, _ := authHarness(t)
	ctx := context.Background()
	raw := []byte(`[{"name":"sid","value":"keep-me","domain":".fixture.test","path":"/","expirationDate":1999999999,"sameSite":"no_restriction"},
		{"name":"g","value":"drop-me","domain":".google.com","path":"/"}]`)
	n, err := h.eng.ImportBrowserSession(ctx, "shop", raw)
	if err != nil || n != 1 {
		t.Fatalf("import: %d %v", n, err)
	}
	st, _ := json.Marshal(h.eng.savedState(ctx, "shop"))
	if !strings.Contains(string(st), "keep-me") || strings.Contains(string(st), "drop-me") {
		t.Fatalf("saved: %s", st)
	}
	if hits := dbContains(t, h, "keep-me"); len(hits) > 0 {
		t.Fatalf("imported session stored in clear in %v", hits)
	}
	if _, err := h.eng.ImportBrowserSession(ctx, "shop", []byte(`[{"name":"g","value":"x","domain":".google.com"}]`)); err == nil {
		t.Fatal("a session with no cookie for the profile was accepted")
	}
	profiles := h.eng.BrowserProfiles(ctx)
	if len(profiles) != 1 || profiles[0]["session_saved"] != true {
		t.Fatalf("profiles %v", profiles)
	}
	_ = time.Second
}
