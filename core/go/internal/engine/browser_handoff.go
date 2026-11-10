package engine

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"aura.local/core/internal/browser"
	"aura.local/core/internal/egress"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

// Workspace handoff (Blueprint D.1 P5 "Workspace handoff", ADR-0042). The owner
// can take over a task's browser session from the Web App: AURA pauses the task
// and its browser tools are refused on that session until the owner explicitly
// returns control; then every element ref is invalidated (AURA must inspect the
// page again) and the task resumes. The same remote view lets the owner log in
// to a login profile by hand (sites that sign in with Google), after which the
// session is saved encrypted for later tasks.
//
// Owner input is the owner acting, not AURA: it needs no approval, is audited
// by action, and typed text is never recorded (only its length).

// ErrNoBrowserSession means there is no open browser session for that key.
var ErrNoBrowserSession = errors.New("no open browser session")

func ownerSessionKey(profile string) string { return "owner-" + profile }

func (e *Engine) sessionByKey(key string) (*brSession, error) {
	if e.brs == nil {
		return nil, ErrBrowserUnavailable
	}
	bs := e.existingBrowserSession(key)
	if bs == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoBrowserSession, key)
	}
	return bs, nil
}

// BrowserTakeover gives the owner control of a task's browser session.
func (e *Engine) BrowserTakeover(ctx context.Context, taskID string) error {
	bs, err := e.sessionByKey(taskID)
	if err != nil {
		return err
	}
	t, err := task.Get(ctx, e.st.R, taskID)
	if err != nil {
		return err
	}
	paused := false
	if !t.State.Terminal() && t.State != task.Paused {
		if _, err := e.Pause(ctx, taskID); err == nil {
			paused = true
		}
	}
	bs.mu.Lock()
	bs.ownerControl, bs.pausedByTakeover = true, paused
	bs.mu.Unlock()
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := task.AppendEvent(ctx, tx, taskID, nil, "BROWSER_TAKEOVER", "owner", map[string]any{"paused": paused}); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "BROWSER_TAKEOVER", "owner", taskID, "", map[string]any{"paused": paused})
	})
}

// BrowserReturn hands control back: refs are invalidated, a logged-in
// profile's session is saved, and a task paused by the takeover resumes.
func (e *Engine) BrowserReturn(ctx context.Context, key string) error {
	bs, err := e.sessionByKey(key)
	if err != nil {
		return err
	}
	var st struct {
		URL string `json:"url"`
	}
	e.brs.client.Call(ctx, "page.wait", map[string]any{"session_id": bs.id, "ms": 0}, &st)
	bs.mu.Lock()
	wasOwner, resume := bs.ownerControl, bs.pausedByTakeover
	bs.ownerControl, bs.pausedByTakeover = false, false
	bs.url, bs.elements, bs.typed = st.URL, nil, nil // policy revalidation: AURA must inspect again
	profile := bs.loggedIn
	bs.mu.Unlock()
	if !wasOwner {
		return fmt.Errorf("%w: the owner does not have control of this session", task.ErrInvalidTransition)
	}
	if profile != "" {
		e.saveState(ctx, bs.id, profile)
	}
	if strings.HasPrefix(key, "owner-") {
		return nil
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := task.AppendEvent(ctx, tx, key, nil, "BROWSER_CONTROL_RETURNED", "owner", map[string]any{"url": st.URL}); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "BROWSER_CONTROL_RETURNED", "owner", key, "", map[string]any{"url": st.URL})
	})
	if resume {
		_, err = e.Resume(ctx, key)
	}
	return err
}

// BrowserScreen returns a PNG of the session's page (owner view).
func (e *Engine) BrowserScreen(ctx context.Context, key string) ([]byte, string, error) {
	bs, err := e.sessionByKey(key)
	if err != nil {
		return nil, "", err
	}
	var shot struct {
		URL       string `json:"url"`
		PNGBase64 string `json:"png_base64"`
	}
	if err := e.brs.client.Call(ctx, "page.screenshot", map[string]any{"session_id": bs.id}, &shot); err != nil {
		return nil, "", browserErr(err)
	}
	png, err := base64.StdEncoding.DecodeString(shot.PNGBase64)
	return png, shot.URL, err
}

// OwnerInput is one remote action by the owner on a session under owner control.
type OwnerInput struct {
	Action    string  `json:"action"` // click | key | type | scroll | goto | back
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Key       string  `json:"key"`
	Text      string  `json:"text"`
	URL       string  `json:"url"`
	Direction string  `json:"direction"`
}

// BrowserInput performs an owner action. The session's egress allowlist still
// applies (the proxy enforces it for every request).
func (e *Engine) BrowserInput(ctx context.Context, key string, in OwnerInput) (string, error) {
	bs, err := e.sessionByKey(key)
	if err != nil {
		return "", err
	}
	bs.mu.Lock()
	owner := bs.ownerControl
	bs.mu.Unlock()
	if !owner {
		return "", fmt.Errorf("%w: take over the session first", task.ErrInvalidTransition)
	}
	call := func(method string, p map[string]any) (string, error) {
		p["session_id"] = bs.id
		var st struct {
			URL string `json:"url"`
		}
		if err := e.brs.client.Call(ctx, method, p, &st); err != nil {
			return "", browserErr(err)
		}
		return st.URL, nil
	}
	audit := map[string]any{"action": in.Action}
	var u string
	switch in.Action {
	case "click":
		audit["x"], audit["y"] = int(in.X), int(in.Y)
		u, err = call("page.mouse_click", map[string]any{"x": in.X, "y": in.Y})
	case "key":
		audit["key"] = in.Key
		u, err = call("page.key", map[string]any{"key": in.Key})
	case "type":
		if len(in.Text) == 0 || len(in.Text) > 2000 {
			return "", fmt.Errorf("%w: text must be 1..2000 characters", ErrInvalid)
		}
		audit["chars"] = len(in.Text) // the text itself is never recorded
		u, err = call("page.keyboard_type", map[string]any{"text": in.Text})
	case "scroll":
		d := in.Direction
		if d != "up" {
			d = "down"
		}
		u, err = call("page.scroll", map[string]any{"direction": d})
	case "back":
		u, err = call("page.back", map[string]any{})
	case "goto":
		pu, perr := url.Parse(in.URL)
		var allow []string
		if es := e.brs.proxy.Session(bs.id); es != nil {
			allow = es.AllowDomain
		}
		if perr != nil || tools.CheckURL(pu, allow) != nil {
			return "", fmt.Errorf("%w: the URL is outside this session's domains", ErrInvalid)
		}
		audit["url"] = in.URL
		u, err = call("page.goto", map[string]any{"url": in.URL})
	default:
		return "", fmt.Errorf("%w: action must be click, key, type, scroll, goto or back", ErrInvalid)
	}
	if err != nil {
		return "", err
	}
	bs.mu.Lock()
	bs.url, bs.elements, bs.typed = u, nil, nil
	bs.mu.Unlock()
	taskID := ""
	if !strings.HasPrefix(key, "owner-") {
		taskID = key
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "BROWSER_OWNER_INPUT", "owner", taskID, "", audit)
	})
	return u, nil
}

// StartProfileLogin opens an owner-controlled session for a login profile, so
// the owner can sign in by hand. extra are additional domains the sign-in
// needs (for example accounts.google.com for "Sign in with Google").
func (e *Engine) StartProfileLogin(ctx context.Context, profile, startURL string, extra []string) (string, error) {
	if e.brs == nil {
		return "", ErrBrowserUnavailable
	}
	l, ok := e.loginProfile(profile)
	if !ok {
		return "", fmt.Errorf("%w: no login profile %q", ErrInvalid, profile)
	}
	domains := append(append([]string{}, l.Domains...), extra...)
	u, err := url.Parse(startURL)
	if err != nil || tools.CheckURL(u, domains) != nil {
		return "", fmt.Errorf("%w: start URL must be https/http on the profile's domains", ErrInvalid)
	}
	key := ownerSessionKey(profile)
	e.closeBrowserSession(key)
	s := &brSession{id: "s-" + key, ownerControl: true, loggedIn: profile}
	tok := randomToken()
	e.brs.proxy.Add(&egress.Session{ID: s.id, Token: tok, AllowDomain: domains})
	open := map[string]any{"session_id": s.id, "proxy_token": tok}
	if st := e.savedState(ctx, profile); st != nil {
		open["storage_state"] = st
	}
	if err := e.brs.client.Call(ctx, "session.open", open, nil); err != nil {
		e.brs.proxy.Remove(s.id)
		return "", browserErr(err)
	}
	e.brs.mu.Lock()
	e.brs.sessions[key] = s
	e.brs.mu.Unlock()
	var st browser.PageState
	if err := e.brs.client.Call(ctx, "page.goto", map[string]any{"session_id": s.id, "url": startURL}, &st); err != nil {
		return key, browserErr(err)
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "BROWSER_PROFILE_LOGIN_STARTED", "owner", "", "", map[string]any{"profile": profile, "domains": domains})
	})
	return key, nil
}

// FinishProfileLogin saves the owner's signed-in session (encrypted, limited to
// the profile's domains) and closes the remote view.
func (e *Engine) FinishProfileLogin(ctx context.Context, profile string, save bool) (bool, error) {
	key := ownerSessionKey(profile)
	bs, err := e.sessionByKey(key)
	if err != nil {
		return false, err
	}
	saved := false
	if save {
		saved = e.saveState(ctx, bs.id, profile)
	}
	e.closeBrowserSession(key)
	e.st.Write(ctx, func(tx *sql.Tx) error {
		return policy.Audit(ctx, tx, "BROWSER_PROFILE_LOGIN_FINISHED", "owner", "", "", map[string]any{"profile": profile, "saved": saved})
	})
	return saved, nil
}

// BrowserSessions lists open sessions the owner can view (tasks and profile logins).
func (e *Engine) BrowserSessions() []map[string]any {
	if e.brs == nil {
		return nil
	}
	e.brs.mu.Lock()
	defer e.brs.mu.Unlock()
	var out []map[string]any
	for key, s := range e.brs.sessions {
		s.mu.Lock()
		out = append(out, map[string]any{"key": key, "url": s.url, "owner_control": s.ownerControl})
		s.mu.Unlock()
	}
	return out
}
