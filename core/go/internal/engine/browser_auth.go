package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"aura.local/core/internal/browser"
	"aura.local/core/internal/config"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
	"aura.local/core/internal/vault"
)

// Browser login, one-time codes, downloads and uploads (Blueprint 8.8.1 rules
// 4 and 7, ADR-0040).
//   - Credentials live in the vault or a 0400 file. The Core types them; the
//     model only chooses which inspected fields receive them and never sees the
//     values. A task may use a profile only if the owner named it at
//     submission, the page is on the profile's domains, the password field is
//     a real password input, and the owner approves this login (A2).
//   - One-time codes reach the browser through a Core-only channel
//     (aura browser code); they are never stored or shown to the model.
//   - A successful login's cookies and storage are saved encrypted in the
//     vault, limited to the profile's domains, and loaded for later tasks of
//     the same profile. Sessions can also be imported (sites that sign in with
//     Google, where the owner logs in in their own browser).
//   - Downloads land in quarantine (the browser container's tmpfs); only an
//     allowed type within the size limit enters the workspace.
//   - Uploads are off unless the owner enables them; each needs an approval
//     bound to the file's hash, the page and the element.

func (e *Engine) loginProfile(name string) (config.LoginProfile, bool) {
	for _, l := range e.cfg.Browser.Logins {
		if l.Name == name {
			return l, true
		}
	}
	return config.LoginProfile{}, false
}

func sessionSecretName(profile string) string { return "browser-session." + profile }

// filterState keeps only the cookies and origins of the profile's domains.
func filterState(raw json.RawMessage, domains []string) (map[string]any, int, error) {
	var st struct {
		Cookies []map[string]any `json:"cookies"`
		Origins []map[string]any `json:"origins"`
	}
	var arr []map[string]any
	if json.Unmarshal(raw, &st) != nil || (st.Cookies == nil && st.Origins == nil) {
		if err := json.Unmarshal(raw, &arr); err != nil { // a plain cookie list (browser extension export)
			return nil, 0, errors.New("session file must be a Playwright storage state or a JSON list of cookies")
		}
		st.Cookies = arr
	}
	out := map[string]any{"cookies": []any{}, "origins": []any{}}
	n := 0
	for _, c := range st.Cookies {
		d, _ := c["domain"].(string)
		if d == "" || !tools.DomainAllowed(strings.TrimPrefix(d, "."), domains) && !tools.DomainAllowed("x."+strings.TrimPrefix(d, "."), domains) {
			continue
		}
		cookie := map[string]any{"name": c["name"], "value": c["value"], "domain": d, "path": firstNonEmpty(str(c["path"]), "/"),
			"secure": c["secure"] == true, "httpOnly": c["httpOnly"] == true}
		if exp, ok := c["expires"].(float64); ok {
			cookie["expires"] = exp
		} else if exp, ok := c["expirationDate"].(float64); ok {
			cookie["expires"] = exp
		} else {
			cookie["expires"] = -1
		}
		if ss, ok := c["sameSite"].(string); ok && (ss == "Strict" || ss == "Lax" || ss == "None") {
			cookie["sameSite"] = ss
		} else {
			cookie["sameSite"] = "Lax"
		}
		out["cookies"] = append(out["cookies"].([]any), cookie)
		n++
	}
	for _, o := range st.Origins {
		u, err := url.Parse(str(o["origin"]))
		if err == nil && tools.DomainAllowed(u.Hostname(), domains) {
			out["origins"] = append(out["origins"].([]any), o)
		}
	}
	return out, n, nil
}

func str(v any) string { s, _ := v.(string); return s }

// ImportBrowserSession stores an owner-exported browser session for a login
// profile (encrypted, limited to the profile's domains).
func (e *Engine) ImportBrowserSession(ctx context.Context, profile string, raw []byte) (int, error) {
	l, ok := e.loginProfile(profile)
	if !ok {
		return 0, fmt.Errorf("%w: no login profile %q", ErrInvalid, profile)
	}
	if e.vault == nil {
		return 0, fmt.Errorf("%w: saving browser sessions needs secrets.master_key_file", ErrInvalid)
	}
	st, n, err := filterState(raw, l.Domains)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if n == 0 {
		return 0, fmt.Errorf("%w: no cookie in the file belongs to the profile's domains %v", ErrInvalid, l.Domains)
	}
	b, _ := json.Marshal(st)
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := e.vault.Put(ctx, tx, sessionSecretName(profile), string(b)); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "BROWSER_SESSION_IMPORTED", "owner", "", "", map[string]any{"profile": profile, "cookies": n})
	})
	return n, err
}

// DeleteBrowserSession forgets a saved session (the next login starts fresh).
func (e *Engine) DeleteBrowserSession(ctx context.Context, profile string) error {
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "delete from secrets where name=?", sessionSecretName(profile)); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "BROWSER_SESSION_DELETED", "owner", "", "", map[string]any{"profile": profile})
	})
}

// BrowserProfiles lists login profiles (never their secrets) and whether a session is saved.
func (e *Engine) BrowserProfiles(ctx context.Context) []map[string]any {
	var out []map[string]any
	for _, l := range e.cfg.Browser.Logins {
		var saved int
		e.st.R.QueryRowContext(ctx, "select count(*) from secrets where name=?", sessionSecretName(l.Name)).Scan(&saved)
		out = append(out, map[string]any{"name": l.Name, "domains": l.Domains, "session_saved": saved > 0})
	}
	return out
}

func (e *Engine) savedState(ctx context.Context, profile string) map[string]any {
	if e.vault == nil || profile == "" {
		return nil
	}
	l, ok := e.loginProfile(profile)
	if !ok {
		return nil
	}
	raw, err := e.vault.Get(ctx, e.st.R, sessionSecretName(profile))
	if err != nil {
		return nil
	}
	st, _, err := filterState(json.RawMessage(raw), l.Domains)
	if err != nil {
		return nil
	}
	return st
}

func (e *Engine) saveState(ctx context.Context, sid, profile string) bool {
	l, ok := e.loginProfile(profile)
	if !ok || e.vault == nil {
		return false
	}
	var out struct {
		StorageState json.RawMessage `json:"storage_state"`
	}
	if err := e.brs.client.Call(ctx, "session.export", map[string]any{"session_id": sid}, &out); err != nil {
		return false
	}
	st, n, err := filterState(out.StorageState, l.Domains)
	if err != nil || n == 0 {
		return false
	}
	b, _ := json.Marshal(st)
	return e.st.Write(ctx, func(tx *sql.Tx) error { return e.vault.Put(ctx, tx, sessionSecretName(profile), string(b)) }) == nil
}

var downloadNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeDownloadName returns a workspace-safe name and its extension.
func safeDownloadName(suggested string) (string, string) {
	name := downloadNameRe.ReplaceAllString(path.Base(strings.ReplaceAll(suggested, "\\", "/")), "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = "download"
	}
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	return name, ext
}

// checkDownload applies the type policy to a quarantined file.
func (e *Engine) checkDownload(ext string, data []byte) error {
	allowed := false
	for _, t := range e.cfg.Browser.DownloadTypes {
		allowed = allowed || strings.EqualFold(t, ext)
	}
	if !allowed {
		return fmt.Errorf("file type .%s is not an allowed download type", ext)
	}
	switch {
	case bytes.HasPrefix(data, []byte("MZ")), bytes.HasPrefix(data, []byte("\x7fELF")), bytes.HasPrefix(data, []byte("#!")),
		bytes.HasPrefix(data, []byte("\xcf\xfa\xed\xfe")), bytes.HasPrefix(data, []byte("\xca\xfe\xba\xbe")):
		return errors.New("the content is an executable, whatever its name")
	case bytes.HasPrefix(data, []byte("PK\x03\x04")) && ext != "docx" && ext != "xlsx":
		return errors.New("archives are not accepted (only .docx/.xlsx, which are zip-based)")
	}
	return nil
}

// textual downloads are previewed for the planner (untrusted content).
func textualExt(ext string) bool {
	switch ext {
	case "csv", "txt", "json", "md", "xml", "html":
		return true
	}
	return false
}

type pendingMFA struct {
	ref, submitRef string
}

// validateBrowserAuthArgs handles the ADR-0040 tools.
func (e *Engine) validateBrowserAuthArgs(s *session, c toolCall, bs *brSession, needRef func(*string) (browser.Element, string)) (map[string]any, string, string) {
	var a struct {
		Ref       *string `json:"ref"`
		Path      *string `json:"path"`
		Profile   *string `json:"profile"`
		UserRef   *string `json:"user_ref"`
		PassRef   *string `json:"pass_ref"`
		SubmitRef *string `json:"submit_ref"`
	}
	if strictDecode(c.Arguments, &a) != nil {
		return nil, "", "invalid_arguments"
	}
	page := ""
	if bs != nil {
		bs.mu.Lock()
		page = bs.url
		bs.mu.Unlock()
	}
	switch c.ToolID {
	case "browser.download":
		if !e.cfg.Browser.Downloads {
			return nil, "", "downloads_disabled"
		}
		el, why := needRef(a.Ref)
		if why != "" {
			return nil, "", why
		}
		return map[string]any{"ref": el.Ref, "url": page, "element": map[string]any{"tag": el.Tag, "name": el.Name, "href": el.Href}}, page, ""
	case "browser.upload":
		if !e.cfg.Browser.UploadsEnabled {
			return nil, "", "uploads_disabled" // Blueprint 8.8.1: off unless the owner enables them
		}
		if a.Path == nil || s.plan == nil || s.plan.Path == nil || *s.plan.Path != *a.Path {
			return nil, "", "arguments_differ_from_plan"
		}
		el, why := needRef(a.Ref)
		if why != "" {
			return nil, "", why
		}
		if el.Tag != "input" || el.Type != "file" {
			return nil, "", "not_a_file_input"
		}
		f, err := e.ws.Read(s.t.ID, *a.Path)
		if err != nil || f.Bytes > 10<<20 {
			return nil, "", "upload_file_unavailable"
		}
		// The approval binds the exact file content, the page and the element.
		return map[string]any{"ref": el.Ref, "path": f.Path, "sha256": f.SHA256, "bytes": f.Bytes, "url": page,
			"element": map[string]any{"tag": el.Tag, "name": el.Name}}, page, ""
	case "browser.login":
		if a.Profile == nil || *a.Profile != s.t.Scope.BrowserProfile || s.t.Scope.BrowserProfile == "" {
			return nil, "", "login_profile_not_granted" // the owner names the profile when submitting the task
		}
		l, ok := e.loginProfile(*a.Profile)
		if !ok {
			return nil, "", "login_profile_not_granted"
		}
		u, err := url.Parse(page)
		if err != nil || !tools.DomainAllowed(u.Hostname(), l.Domains) || u.Scheme != "https" {
			return nil, "", "login_page_not_on_profile_domains" // credentials are typed only on the profile's https pages
		}
		user, why := needRef(a.UserRef)
		if why != "" {
			return nil, "", why
		}
		pass, why := needRef(a.PassRef)
		if why != "" {
			return nil, "", why
		}
		if pass.Tag != "input" || pass.Type != "password" {
			return nil, "", "password_field_is_not_a_password_input"
		}
		submit, why := needRef(a.SubmitRef)
		if why != "" {
			return nil, "", why
		}
		return map[string]any{"profile": l.Name, "url": page,
			"user":   map[string]any{"ref": user.Ref, "name": user.Name},
			"pass":   map[string]any{"ref": pass.Ref, "name": pass.Name},
			"submit": map[string]any{"ref": submit.Ref, "name": submit.Name}}, page, ""
	case "browser.mfa":
		if bs == nil || bs.loggedIn == "" {
			return nil, "", "mfa_without_login" // only to finish a login approved in this task
		}
		el, why := needRef(a.Ref)
		if why != "" {
			return nil, "", why
		}
		out := map[string]any{"ref": el.Ref, "url": page}
		if a.SubmitRef != nil && *a.SubmitRef != "" {
			sub, why := needRef(a.SubmitRef)
			if why != "" {
				return nil, "", why
			}
			out["submit_ref"] = sub.Ref
		}
		return out, page, ""
	}
	return nil, "", "unknown_tool"
}

// executeBrowserAuth performs download, upload and login; it returns the
// observation content and page state.
func (e *Engine) executeBrowserAuth(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any,
	bs *brSession, call func(string, map[string]any, any) error, summary map[string]any) (string, browser.PageState, error) {
	var state browser.PageState
	switch spec.ID {
	case "browser.download":
		var dl struct {
			URL               string `json:"url"`
			SuggestedFilename string `json:"suggested_filename"`
			ContentBase64     string `json:"content_base64"`
		}
		if err := call("page.download", map[string]any{"ref": args["ref"], "max_bytes": e.cfg.Browser.MaxDownloadMB << 20}, &dl); err != nil {
			return "", state, browserErr(err)
		}
		data, err := base64.StdEncoding.DecodeString(dl.ContentBase64)
		if err != nil {
			return "", state, errors.New("tool_failed: bad download")
		}
		name, ext := safeDownloadName(dl.SuggestedFilename)
		if err := e.checkDownload(ext, data); err != nil {
			return "", state, fmt.Errorf("tool_failed: download_rejected: %s %v (kept out of the workspace)", name, err)
		}
		dest := "downloads/" + name
		for i := 2; ; i++ {
			if _, err := e.ws.Read(t.ID, dest); err != nil {
				break
			}
			dest = fmt.Sprintf("downloads/%d-%s", i, name)
		}
		w, err := e.ws.Write(t.ID, dest, string(data))
		if err != nil {
			return "", state, fmt.Errorf("tool_failed: %v", err)
		}
		summary["path"], summary["bytes"], summary["sha256"] = w.Path, len(data), w.SHA256
		content := fmt.Sprintf("Downloaded %s from %s into %s (%d bytes, sha256 %s).\n", name, dl.URL, w.Path, len(data), w.SHA256[:16])
		if textualExt(ext) {
			content += "\nBeginning of the file (untrusted content):\n" + truncate(string(data), 4000)
		}
		return content, state, nil
	case "browser.upload":
		el, _ := args["element"].(map[string]any)
		f, err := e.ws.Read(t.ID, str(args["path"]))
		if err != nil || f.SHA256 != str(args["sha256"]) {
			return "", state, errors.New("tool_failed: approval_mismatch: the file changed after approval")
		}
		var cur struct{ URL, Tag, Name string }
		if err := call("page.describe", map[string]any{"ref": args["ref"]}, &cur); err != nil {
			return "", state, browserErr(err)
		}
		if cur.URL != args["url"] || cur.Tag != el["tag"] || cur.Name != el["name"] {
			return "", state, errors.New("tool_failed: approval_mismatch: the page or element changed after approval")
		}
		if err := e.recordBrowserIntent(ctx, t, st, spec, args); err != nil {
			return "", state, err
		}
		if err := call("page.upload", map[string]any{"ref": args["ref"], "filename": path.Base(f.Path),
			"content_base64": base64.StdEncoding.EncodeToString([]byte(f.Content))}, nil); err != nil {
			return "", state, browserErr(err)
		}
		return fmt.Sprintf("Attached %s (%d bytes) to %v on %v; not submitted yet.\n", f.Path, f.Bytes, el["name"], args["url"]), state, nil
	case "browser.login":
		l, ok := e.loginProfile(str(args["profile"]))
		if !ok || e.secret == nil {
			return "", state, errors.New("tool_failed: login profile unavailable")
		}
		var cur struct{ URL string }
		if err := call("page.describe", map[string]any{"ref": args["submit"].(map[string]any)["ref"]}, &cur); err != nil {
			return "", state, browserErr(err)
		}
		if cur.URL != args["url"] {
			return "", state, errors.New("tool_failed: approval_mismatch: the login page changed after approval")
		}
		username := l.Username
		if strings.HasPrefix(username, vault.RefPrefix) {
			v, err := e.secret(ctx, username)
			if err != nil {
				return "", state, errors.New("tool_failed: login username unavailable")
			}
			username = v
		}
		password, err := e.secret(ctx, l.PasswordRef)
		if err != nil {
			return "", state, errors.New("tool_failed: login password unavailable")
		}
		if err := e.recordBrowserIntent(ctx, t, st, spec, args); err != nil {
			return "", state, err
		}
		// The Core types the credentials; they never reach the worker, the model or the trace.
		if err := call("page.type", map[string]any{"ref": args["user"].(map[string]any)["ref"], "text": username}, nil); err != nil {
			return "", state, browserErr(err)
		}
		if err := call("page.type", map[string]any{"ref": args["pass"].(map[string]any)["ref"], "text": password}, nil); err != nil {
			return "", state, browserErr(err)
		}
		if err := call("page.click", map[string]any{"ref": args["submit"].(map[string]any)["ref"]}, &state); err != nil {
			return "", state, browserErr(err)
		}
		bs.mu.Lock()
		bs.url, bs.elements, bs.typed, bs.loggedIn = state.URL, nil, nil, l.Name
		bs.mu.Unlock()
		saved := e.saveState(ctx, bs.id, l.Name)
		summary["profile"], summary["session_saved"] = l.Name, saved
		note := "The session was saved encrypted for later tasks of this profile."
		if !saved {
			note = "The session was not saved (no vault, or no cookie for the profile's domains)."
		}
		return fmt.Sprintf("Login submitted with profile %s. Now at %s\nTitle: %s\n%s If the site asks for a one-time code, "+
			"use browser.mfa on the code field; the owner types the code through the Core.\n", l.Name, state.URL, state.Title, note), state, nil
	}
	return "", state, errors.New("tool_failed: unknown browser tool")
}

func (e *Engine) recordBrowserIntent(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) error {
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "external_intent", externalIntent{ToolID: spec.ID, Arguments: args})
		if err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_INTENT", map[string]any{"tool_id": spec.ID, "side_effect": spec.SideEffect, "url": args["url"]})
	})
}

// startMFA parks the step until the owner gives the one-time code through the
// Core-only channel; the planner is told how, never the code.
func (e *Engine) startMFA(ctx context.Context, t *task.Task, st *task.Step, args map[string]any) (string, error) {
	e.brs.mu.Lock()
	if e.brs.mfa == nil {
		e.brs.mfa = map[string]pendingMFA{}
	}
	e.brs.mfa[t.ID] = pendingMFA{ref: str(args["ref"]), submitRef: str(args["submit_ref"])}
	e.brs.mu.Unlock()
	id, err := e.createWait(ctx, t, st, "wait.event", map[string]any{"key": "mfa:" + t.ID})
	if err != nil {
		return "", err
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "insert into notifications(id, kind, task_id, title, body_json, created_at) values(?,?,?,?,?,?)",
			ids.New(), "info", t.ID, "A site asks for a one-time code", js(map[string]any{"page": args["url"],
				"how": "aura browser code " + t.ID + " <code>   (the code goes straight into the page; it is not stored)"}), ids.NowMs())
		return err
	})
	return id, nil
}

// BrowserCode types the owner's one-time code into the waiting page and wakes
// the task. The code is not stored, traced or shown to the model.
func (e *Engine) BrowserCode(ctx context.Context, taskID, code string) error {
	if e.brs == nil {
		return ErrBrowserUnavailable
	}
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 64 {
		return fmt.Errorf("%w: code must be 1..64 characters", ErrInvalid)
	}
	e.brs.mu.Lock()
	p, ok := e.brs.mfa[taskID]
	e.brs.mu.Unlock()
	bs := e.existingBrowserSession(taskID)
	if !ok || bs == nil {
		return fmt.Errorf("%w: this task is not waiting for a one-time code (or its browser session ended)", task.ErrInvalidTransition)
	}
	if err := e.brs.client.Call(ctx, "page.type", map[string]any{"session_id": bs.id, "ref": p.ref, "text": code}, nil); err != nil {
		return browserErr(err)
	}
	var state browser.PageState
	if p.submitRef != "" {
		if err := e.brs.client.Call(ctx, "page.click", map[string]any{"session_id": bs.id, "ref": p.submitRef}, &state); err != nil {
			return browserErr(err)
		}
		bs.mu.Lock()
		bs.url, bs.elements, bs.typed = state.URL, nil, nil
		bs.mu.Unlock()
	}
	e.brs.mu.Lock()
	delete(e.brs.mfa, taskID)
	e.brs.mu.Unlock()
	e.st.Write(ctx, func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "BROWSER_MFA_CODE_ENTERED", "owner", taskID, "", map[string]any{"submitted": p.submitRef != ""})
	})
	if bs.loggedIn != "" {
		e.saveState(ctx, bs.id, bs.loggedIn)
	}
	payload, _ := json.Marshal(map[string]any{"result": "the owner's one-time code was entered by the Core", "url": state.URL, "title": state.Title})
	_, _, err := e.ExternalEvent(ctx, "mfa:"+taskID, ids.New(), payload)
	return err
}
