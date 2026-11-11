package engine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"aura.local/core/internal/browser"
	"aura.local/core/internal/egress"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

// Browser automation v1 (Blueprint 8.8.1, ADR-0039). The Core owns every
// browser session: one per task (its own BrowserContext: cookies and storage
// isolated), with egress credentials that only the Core's proxy accepts and
// only for the task's domains. Element refs come from the Core's last inspect
// of that page, so the model can only act on elements the Core has seen.

type brSession struct {
	mu       sync.Mutex
	id       string
	url      string
	elements []browser.Element
	typed    []map[string]any // type/select on the current page since the last navigation
	loggedIn string           // login profile used successfully in this session (ADR-0040)
	// ownerControl: the owner has taken over (ADR-0042); AURA's browser tools are refused.
	ownerControl, pausedByTakeover bool
}

type browserState struct {
	client   *browser.Client
	proxy    *egress.Proxy
	proxyURL string
	mu       sync.Mutex
	sessions map[string]*brSession // by task id
	mfa      map[string]pendingMFA // tasks waiting for a one-time code, by task id
}

// ErrBrowserUnavailable means browser automation is not configured.
var ErrBrowserUnavailable = errors.New("tool_failed: browser automation is not configured")

// SetBrowser enables browser tools (app wiring).
func (e *Engine) SetBrowser(c *browser.Client, p *egress.Proxy) {
	e.brs = &browserState{client: c, proxy: p, sessions: map[string]*brSession{}}
	e.CloseAllBrowserSessions() // sessions of a previous Core run are not resumable
}

// SetSearcher replaces the web.search provider (app wiring: the Chromium
// searcher needs the browser, which is set up after the engine).
func (e *Engine) SetSearcher(s tools.Searcher) { e.srch = s }

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (e *Engine) browserSession(ctx context.Context, t *task.Task) (*brSession, error) {
	if e.brs == nil {
		return nil, ErrBrowserUnavailable
	}
	e.brs.mu.Lock()
	defer e.brs.mu.Unlock()
	if s, ok := e.brs.sessions[t.ID]; ok {
		return s, nil
	}
	s := &brSession{id: "s-" + t.ID}
	tok := randomToken()
	e.brs.proxy.Add(&egress.Session{ID: s.id, Token: tok, TaskID: t.ID, AllowDomain: append([]string{}, t.Scope.AllowDomains...)})
	open := map[string]any{"session_id": s.id, "proxy_token": tok,
		"downloads": e.cfg.Browser.Downloads && slices.Contains(t.Scope.Tools, "browser.download")}
	if st := e.savedState(ctx, t.Scope.BrowserProfile); st != nil {
		open["storage_state"] = st // the owner's saved login for this task's profile
	}
	if err := e.brs.client.Call(ctx, "session.open", open, nil); err != nil {
		e.brs.proxy.Remove(s.id)
		return nil, err
	}
	e.brs.sessions[t.ID] = s
	return s, nil
}

func (e *Engine) existingBrowserSession(taskID string) *brSession {
	if e.brs == nil {
		return nil
	}
	e.brs.mu.Lock()
	defer e.brs.mu.Unlock()
	return e.brs.sessions[taskID]
}

// closeBrowserSession ends a task's session (task finished or stopped).
func (e *Engine) closeBrowserSession(taskID string) {
	if e.brs == nil {
		return
	}
	e.brs.mu.Lock()
	s := e.brs.sessions[taskID]
	delete(e.brs.sessions, taskID)
	e.brs.mu.Unlock()
	if s == nil {
		return
	}
	e.brs.proxy.Remove(s.id)
	e.brs.client.Call(context.Background(), "session.close", map[string]any{"session_id": s.id}, nil)
}

// CloseAllBrowserSessions ends every session (kill switch, startup).
func (e *Engine) CloseAllBrowserSessions() {
	if e.brs == nil {
		return
	}
	e.brs.mu.Lock()
	e.brs.sessions = map[string]*brSession{}
	e.brs.mu.Unlock()
	e.brs.proxy.RemoveAll()
	e.brs.client.Call(context.Background(), "session.close_all", map[string]any{}, nil)
}

func findElement(s *brSession, ref string) (browser.Element, bool) {
	for _, el := range s.elements {
		if el.Ref == ref {
			return el, true
		}
	}
	return browser.Element{}, false
}

// validateBrowserArgs binds browser arguments to the plan, the task's domains
// and the Core's last view of the page.
func (e *Engine) validateBrowserArgs(s *session, c toolCall) (map[string]any, string, string) {
	if e.brs == nil {
		return nil, "", "browser_unavailable"
	}
	if bs := e.existingBrowserSession(s.t.ID); bs != nil {
		bs.mu.Lock()
		owner := bs.ownerControl
		bs.mu.Unlock()
		if owner {
			return nil, "", "owner_has_control" // AURA does not act while the owner controls the page
		}
	}
	switch c.ToolID {
	case "browser.download", "browser.upload", "browser.login", "browser.mfa":
		bs := e.existingBrowserSession(s.t.ID)
		return e.validateBrowserAuthArgs(s, c, bs, func(ref *string) (browser.Element, string) {
			if ref == nil || bs == nil {
				return browser.Element{}, "stale_ref"
			}
			bs.mu.Lock()
			defer bs.mu.Unlock()
			el, ok := findElement(bs, *ref)
			if !ok {
				return browser.Element{}, "stale_ref"
			}
			return el, ""
		})
	}
	var a struct {
		URL       *string `json:"url"`
		Ref       *string `json:"ref"`
		Text      *string `json:"text"`
		Value     *string `json:"value"`
		Direction *string `json:"direction"`
		MS        *int    `json:"ms"`
	}
	if strictDecode(c.Arguments, &a) != nil {
		return nil, "", "invalid_arguments"
	}
	if e.brs == nil {
		return nil, "", "browser_unavailable"
	}
	planned := map[string]any{}
	if s.plan != nil && s.plan.Input != nil {
		json.Unmarshal([]byte(*s.plan.Input), &planned)
	}
	bs := e.existingBrowserSession(s.t.ID)
	needRef := func() (browser.Element, string) {
		if a.Ref == nil || bs == nil {
			return browser.Element{}, "stale_ref"
		}
		bs.mu.Lock()
		defer bs.mu.Unlock()
		el, ok := findElement(bs, *a.Ref)
		if !ok {
			return browser.Element{}, "stale_ref" // only elements from the Core's last inspect of this page
		}
		return el, ""
	}
	switch c.ToolID {
	case "browser.open":
		if a.URL == nil || *a.URL == "" {
			return nil, "", "invalid_arguments"
		}
		if s.plan == nil {
			return nil, "", "arguments_differ_from_plan"
		}
		if s.plan.URL != nil && *s.plan.URL != "" {
			if *s.plan.URL != *a.URL {
				return nil, "", "arguments_differ_from_plan"
			}
		} else if !e.hasProvenance(s.t, *a.URL) {
			return nil, "", "argument_without_provenance"
		}
		u, err := url.Parse(*a.URL)
		if err != nil || tools.CheckURL(u, s.t.Scope.AllowDomains) != nil {
			return nil, "", "domain_not_allowed"
		}
		return map[string]any{"url": *a.URL}, *a.URL, ""
	case "browser.inspect", "browser.screenshot", "browser.back":
		return map[string]any{}, "", ""
	case "browser.scroll":
		d := "down"
		if a.Direction != nil {
			d = *a.Direction
		}
		if d != "up" && d != "down" {
			return nil, "", "invalid_arguments"
		}
		return map[string]any{"direction": d}, "", ""
	case "browser.wait":
		ms := 1000
		if a.MS != nil {
			ms = *a.MS
		}
		if ms < 0 || ms > 10000 {
			return nil, "", "invalid_arguments"
		}
		return map[string]any{"ms": ms}, "", ""
	case "browser.type", "browser.select":
		field, val := "text", a.Text
		if c.ToolID == "browser.select" {
			field, val = "value", a.Value
		}
		if val == nil || planned[field] != *val {
			return nil, "", "arguments_differ_from_plan" // the value comes from the plan, never from page content
		}
		el, why := needRef()
		if why != "" {
			return nil, "", why
		}
		return map[string]any{"ref": el.Ref, field: *val, "element": el}, "", ""
	case "browser.click":
		el, why := needRef()
		if why != "" {
			return nil, "", why
		}
		bs.mu.Lock()
		page, typed := bs.url, append([]map[string]any{}, bs.typed...)
		bs.mu.Unlock()
		// The approval covers the page, the element and every value typed on
		// this page: if any of them changes, the action hash changes.
		return map[string]any{"ref": el.Ref, "url": page, "element": map[string]any{"tag": el.Tag, "name": el.Name, "href": el.Href},
			"typed": typed}, page, ""
	}
	return nil, "", "unknown_tool"
}

func elementLine(el browser.Element) string {
	s := fmt.Sprintf("%s [%s", el.Ref, el.Tag)
	if el.Type != "" {
		s += " type=" + el.Type
	}
	if el.Role != "" {
		s += " role=" + el.Role
	}
	s += "] " + el.Name
	if el.Href != "" {
		s += " → " + el.Href
	}
	return s
}

func browserErr(err error) error {
	var be *browser.Error
	if errors.As(err, &be) {
		return fmt.Errorf("tool_failed: browser %s: %s", be.Code, truncate(be.Message, 200))
	}
	return fmt.Errorf("tool_failed: %v", err)
}

// executeBrowser performs one validated browser action in the task's session.
func (e *Engine) executeBrowser(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	fail := func(err error) (any, error) {
		e.toolFailed(ctx, t, st, spec.ID, err)
		return nil, err
	}
	bs, err := e.browserSession(ctx, t)
	if err != nil {
		return fail(browserErr(err))
	}
	sid := bs.id
	call := func(method string, params map[string]any, out any) error {
		params["session_id"] = sid
		return e.brs.client.Call(ctx, method, params, out)
	}
	traced := map[string]any{"tool_id": spec.ID, "idempotency_key": st.IdempotencyKey}
	for _, k := range []string{"url", "ref", "direction", "ms"} {
		if v, ok := args[k]; ok {
			traced[k] = v
		}
	}
	if v, ok := args["text"].(string); ok {
		traced["typed_chars"] = len(v) // the value itself is not traced
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_STARTED", "core", traced)
		return err
	})
	var state browser.PageState
	var content string
	summary := map[string]any{"tool_id": spec.ID}
	newPage := func() {
		bs.mu.Lock()
		bs.url, bs.elements, bs.typed = state.URL, nil, nil
		bs.mu.Unlock()
	}
	switch spec.ID {
	case "browser.open":
		if err := call("page.goto", map[string]any{"url": args["url"]}, &state); err != nil {
			return fail(browserErr(err))
		}
		newPage()
		content = fmt.Sprintf("Opened %s\nTitle: %s\n", state.URL, state.Title)
	case "browser.back":
		if err := call("page.back", map[string]any{}, &state); err != nil {
			return fail(browserErr(err))
		}
		newPage()
		content = fmt.Sprintf("Back to %s\nTitle: %s\n", state.URL, state.Title)
	case "browser.inspect":
		if err := call("page.inspect", map[string]any{"max_chars": 12000}, &state); err != nil {
			return fail(browserErr(err))
		}
		bs.mu.Lock()
		bs.url, bs.elements = state.URL, state.Elements
		bs.mu.Unlock()
		var sb strings.Builder
		fmt.Fprintf(&sb, "Page %s\nTitle: %s\n\nVisible text:\n%s\n\nInteractive elements (use their ref):\n", state.URL, state.Title, state.Text)
		for _, el := range state.Elements {
			sb.WriteString(elementLine(el) + "\n")
		}
		content = sb.String()
		summary["elements"] = len(state.Elements)
	case "browser.click":
		el, _ := args["element"].(map[string]any)
		var cur struct{ URL, Tag, Name, Href string }
		if err := call("page.describe", map[string]any{"ref": args["ref"]}, &cur); err != nil {
			return fail(browserErr(err))
		}
		bs.mu.Lock()
		typedNow, _ := json.Marshal(bs.typed)
		bs.mu.Unlock()
		typedApproved, _ := json.Marshal(args["typed"])
		if cur.URL != args["url"] || cur.Tag != el["tag"] || cur.Name != el["name"] || string(typedNow) != string(typedApproved) {
			return fail(errors.New("tool_failed: approval_mismatch: the page, element or typed values changed after approval"))
		}
		// An external side effect: the intent is durable before the click, so a
		// crash leaves an unknown outcome for the owner, never a blind resubmit.
		err := e.st.Write(ctx, func(tx *sql.Tx) error {
			ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "external_intent", externalIntent{ToolID: spec.ID, Arguments: args})
			if err != nil {
				return err
			}
			return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_INTENT", map[string]any{"tool_id": spec.ID, "side_effect": spec.SideEffect, "url": args["url"]})
		})
		if err != nil {
			return nil, err
		}
		if err := call("page.click", map[string]any{"ref": args["ref"]}, &state); err != nil {
			return fail(browserErr(err))
		}
		newPage()
		content = fmt.Sprintf("Clicked %v on %v\nNow at %s\nTitle: %s\n", el["name"], args["url"], state.URL, state.Title)
	case "browser.download", "browser.upload", "browser.login":
		var err error
		content, state, err = e.executeBrowserAuth(ctx, t, st, spec, args, bs, call, summary)
		if err != nil {
			return fail(err)
		}
	case "browser.type", "browser.select":
		method, field := "page.type", "text"
		if spec.ID == "browser.select" {
			method, field = "page.select", "value"
		}
		if err := call(method, map[string]any{"ref": args["ref"], field: args[field]}, nil); err != nil {
			return fail(browserErr(err))
		}
		el, _ := args["element"].(browser.Element)
		bs.mu.Lock()
		bs.typed = append(bs.typed, map[string]any{"ref": args["ref"], "name": el.Name, field: args[field]})
		bs.mu.Unlock()
		content = fmt.Sprintf("Set %s (%s) on the page (not submitted).\n", args["ref"], el.Name)
	case "browser.scroll", "browser.wait":
		p := map[string]any{}
		for _, k := range []string{"direction", "ms"} {
			if v, ok := args[k]; ok {
				p[k] = v
			}
		}
		if err := call(strings.Replace(spec.ID, "browser.", "page.", 1), p, nil); err != nil {
			return fail(browserErr(err))
		}
		content = spec.ID + " done.\n"
	case "browser.screenshot":
		var shot struct {
			URL       string `json:"url"`
			PNGBase64 string `json:"png_base64"`
		}
		if err := call("page.screenshot", map[string]any{}, &shot); err != nil {
			return fail(browserErr(err))
		}
		png, err := base64.StdEncoding.DecodeString(shot.PNGBase64)
		if err != nil {
			return fail(fmt.Errorf("tool_failed: bad screenshot"))
		}
		path := fmt.Sprintf("browser/screenshot-%d.png", st.StepNo)
		if _, err := e.ws.Write(t.ID, path, string(png)); err != nil {
			return fail(fmt.Errorf("tool_failed: %v", err))
		}
		content = fmt.Sprintf("Screenshot of %s saved to %s (%d bytes).\n", shot.URL, path, len(png))
		summary["path"] = path
	}
	if state.SiteBlocked {
		// Rule 6a: stop, record SITE_BLOCKED, offer only legitimate alternatives.
		return fail(fmt.Errorf("site_blocked: %s shows a bot wall, CAPTCHA or access-denied page; AURA does not bypass it. "+
			"Alternatives: an official API or connector, or you complete this step yourself", state.URL))
	}
	if state.Status != nil && *state.Status >= 400 {
		// An error page (including the egress proxy's refusal) is not the requested content.
		return fail(fmt.Errorf("tool_failed: HTTP %d from %s", *state.Status, state.URL))
	}
	if state.URL != "" {
		summary["url"], summary["title"] = state.URL, truncate(state.Title, 200)
	}
	rec := obsRecord{Source: spec.ID + ":" + firstNonEmpty(state.URL, bs.url), Content: truncate(content, maxObservationChars)}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
		if err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}

// RecordEgressDenial puts a refused browser connection in the task's trace
// (BRW-02 evidence: what the page tried to reach and why it was refused).
func (e *Engine) RecordEgressDenial(d egress.Denial) {
	if !strings.HasPrefix(d.SessionID, "s-") {
		return // owner diagnostics (aura browser check) report their own counts
	}
	taskID := strings.TrimPrefix(d.SessionID, "s-")
	e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := task.AppendEvent(context.Background(), tx, taskID, nil, "EGRESS_DENIED", "core",
			map[string]any{"host": truncate(d.Host, 200), "reason": d.Reason})
		return err
	})
}

// BrowserCheck is an owner diagnostic (no model): open a URL in a fresh,
// isolated session through the real egress proxy, inspect it, and report
// timings, element count and egress decisions; the session is then closed.
func (e *Engine) BrowserCheck(ctx context.Context, rawURL string, domains []string) (map[string]any, error) {
	if e.brs == nil {
		return nil, ErrBrowserUnavailable
	}
	u, err := url.Parse(rawURL)
	if err != nil || tools.CheckURL(u, domains) != nil {
		return nil, fmt.Errorf("%w: url must be http(s) and inside allow_domains", ErrInvalid)
	}
	id := "check-" + randomToken()[:16]
	tok := randomToken()
	es := &egress.Session{ID: id, Token: tok, AllowDomain: domains}
	e.brs.proxy.Add(es)
	defer e.brs.proxy.Remove(id)
	start := time.Now()
	if err := e.brs.client.Call(ctx, "session.open", map[string]any{"session_id": id, "proxy_token": tok}, nil); err != nil {
		return nil, browserErr(err)
	}
	defer e.brs.client.Call(context.Background(), "session.close", map[string]any{"session_id": id}, nil)
	opened := time.Since(start)
	var st browser.PageState
	if err := e.brs.client.Call(ctx, "page.goto", map[string]any{"session_id": id, "url": rawURL}, &st); err != nil {
		return nil, browserErr(err)
	}
	loaded := time.Since(start)
	var in browser.PageState
	if err := e.brs.client.Call(ctx, "page.inspect", map[string]any{"session_id": id, "max_chars": 500}, &in); err != nil {
		return nil, browserErr(err)
	}
	return map[string]any{"url": st.URL, "title": st.Title, "status": st.Status, "site_blocked": st.SiteBlocked,
		"elements": len(in.Elements), "text_sample": truncate(in.Text, 300),
		"session_open_ms": opened.Milliseconds(), "page_loaded_ms": loaded.Milliseconds(),
		"egress_allowed": es.Allowed.Load(), "egress_denied": es.Denied.Load()}, nil
}

// renderPage reads one page in the isolated Chromium for background work (a
// watch whose source needs JavaScript, ADR-0062): a fresh session that can
// reach only allow, the page's visible text, and the session closed again.
func (e *Engine) renderPage(ctx context.Context, raw string, allow []string) (string, error) {
	if e.brs == nil {
		return "", ErrBrowserUnavailable
	}
	u, err := url.Parse(raw)
	if err != nil || tools.CheckURL(u, allow) != nil {
		return "", fmt.Errorf("%w: url must be http(s) and inside allow_domains", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	id, tok := "w-"+randomToken()[:24], randomToken()
	e.brs.proxy.Add(&egress.Session{ID: id, Token: tok, AllowDomain: append([]string{}, allow...)})
	defer e.brs.proxy.Remove(id)
	if err := e.brs.client.Call(ctx, "session.open", map[string]any{"session_id": id, "proxy_token": tok}, nil); err != nil {
		return "", err
	}
	defer e.brs.client.Call(context.WithoutCancel(ctx), "session.close", map[string]any{"session_id": id}, nil)
	var st browser.PageState
	if err := e.brs.client.Call(ctx, "page.goto", map[string]any{"session_id": id, "url": raw}, &st); err != nil {
		return "", err
	}
	if st.SiteBlocked || (st.Status != nil && *st.Status >= 400) {
		return "", fmt.Errorf("tool_failed: %s refused the browser", u.Hostname())
	}
	e.brs.client.Call(ctx, "page.wait", map[string]any{"session_id": id, "ms": 1500}, nil)
	var page browser.PageState
	if err := e.brs.client.Call(ctx, "page.inspect", map[string]any{"session_id": id, "max_chars": 20000}, &page); err != nil {
		return "", err
	}
	return page.Text, nil
}
