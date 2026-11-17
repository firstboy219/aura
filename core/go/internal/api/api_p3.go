package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"aura.local/core/internal/engine"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// P3 owner-facing endpoints: memory, skills, profile (and, in later
// milestones, self-model, scorecard, triggers, notifications).

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if text := q.Get("q"); text != "" {
		ms, err := s.Engine.SearchMemories(r.Context(), text, true, 20)
		if err != nil {
			s.mapErr(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]any{"memories": ms})
		return
	}
	ms, err := s.Engine.ListMemories(r.Context(), q.Get("status"), 200)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"memories": ms})
}

func (s *Server) addMemory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content       string `json:"content"`
		Private       bool   `json:"private"`
		ExpiresInDays int    `json:"expires_in_days"` // ODI A7: 0 = never
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	m, err := s.Engine.AddOwnerMemory(r.Context(), in.Content, in.Private)
	if err == nil && in.ExpiresInDays > 0 {
		m, err = s.Engine.SetMemoryExpiry(r.Context(), m.ID, in.ExpiresInDays)
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 201, m)
}

func (s *Server) correctMemory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content string `json:"content"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	m, err := s.Engine.CorrectMemory(r.Context(), r.PathValue("id"), in.Content)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.DeleteMemory(r.Context(), r.PathValue("id")); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": r.PathValue("id")})
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	sk, err := s.Engine.ListSkills(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"skills": sk})
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sk, err := s.Engine.GetSkill(r.Context(), id)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	vs, _ := s.Engine.SkillVersions(r.Context(), id)
	out := map[string]any{"skill": sk, "versions": vs}
	q := r.URL.Query()
	if from, to := q.Get("diff_from"), q.Get("diff_to"); from != "" && to != "" {
		f, _ := strconv.Atoi(from)
		t, _ := strconv.Atoi(to)
		d, err := s.Engine.SkillDiff(r.Context(), id, f, t)
		if err != nil {
			s.mapErr(w, r, err)
			return
		}
		out["diff"] = d
	}
	writeJSON(w, 200, out)
}

func (s *Server) importSkill(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string   `json:"name"`
		Triggers []string `json:"triggers"`
		Notes    string   `json:"notes"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	sk, err := s.Engine.ImportSkill(r.Context(), in.Name, in.Triggers, in.Notes)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, sk)
}

func (s *Server) skillAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.PathValue("action") {
	case "disable", "enable":
		status := map[string]string{"disable": "disabled", "enable": "active"}[r.PathValue("action")]
		sk, err := s.Engine.SetSkillStatus(r.Context(), id, status)
		if err != nil {
			s.mapErr(w, r, err)
			return
		}
		writeJSON(w, 200, sk)
	case "rollback":
		var in struct {
			Version int `json:"version"`
		}
		if err := decodeStrict(r, &in); err != nil {
			s.mapErr(w, r, err)
			return
		}
		sk, err := s.Engine.RollbackSkill(r.Context(), id, in.Version)
		if err != nil {
			s.mapErr(w, r, err)
			return
		}
		writeJSON(w, 200, sk)
	default:
		s.fail(w, r, 404, "not_found", "no such action")
	}
}

func (s *Server) listProfile(w http.ResponseWriter, r *http.Request) {
	items, err := s.Engine.ListProfile(r.Context(), r.URL.Query().Get("all") == "true")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"profile": items})
}

func (s *Server) addProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Statement string `json:"statement"`
		Scope     string `json:"scope"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	p, err := s.Engine.AddPreference(r.Context(), in.Statement, in.Scope)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) profileAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Statement string `json:"statement"`
	}
	if r.ContentLength > 0 {
		if err := decodeStrict(r, &in); err != nil {
			s.mapErr(w, r, err)
			return
		}
	}
	p, err := s.Engine.UpdateProfile(r.Context(), r.PathValue("id"), r.PathValue("action"), in.Statement)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) selfModel(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.SelfModel(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) scorecard(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.Scorecard(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) shadow(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.ShadowReport(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) self(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.SelfDescription(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}

func (s *Server) listLimitations(w http.ResponseWriter, r *http.Request) {
	ls, err := s.Engine.ListLimitations(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"limitations": ls})
}

func (s *Server) addLimitation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Category  string `json:"category"`
		TaskClass string `json:"task_class"`
		Title     string `json:"title"`
		Note      string `json:"note"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	l, err := s.Engine.AddLimitation(r.Context(), in.Category, in.TaskClass, in.Title, in.Note)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 201, l)
}

func (s *Server) limitationAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength > 0 {
		if err := decodeStrict(r, &in); err != nil {
			s.mapErr(w, r, err)
			return
		}
	}
	l, err := s.Engine.LimitationAction(r.Context(), r.PathValue("id"), r.PathValue("action"), in.Note)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, l)
}

// improvementRun runs one pass of the self-improvement loop now (owner-triggered);
// the loop also runs by itself every 30 s at the lowest priority.
func (s *Server) improvementRun(w http.ResponseWriter, r *http.Request) {
	s.Engine.RunImprovementCycle(r.Context())
	ls, _ := s.Engine.ListLimitations(r.Context(), "open")
	writeJSON(w, 200, map[string]any{"open_limitations": len(ls)})
}

func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Engine.ListTriggers(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"triggers": ts})
}

func (s *Server) createTrigger(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string             `json:"name"`
		Kind            string             `json:"kind"`
		IntervalSeconds *int64             `json:"interval_seconds"`
		DailyAt         *string            `json:"daily_at"`
		Objective       string             `json:"objective"`
		Spec            engine.TriggerSpec `json:"spec"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	t, err := s.Engine.CreateTrigger(r.Context(), &engine.Trigger{Name: in.Name, Kind: in.Kind, IntervalSeconds: in.IntervalSeconds,
		DailyAt: in.DailyAt, Objective: in.Objective, Spec: in.Spec})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 201, t)
}

func (s *Server) triggerAction(w http.ResponseWriter, r *http.Request) {
	t, err := s.Engine.TriggerAction(r.Context(), r.PathValue("id"), r.PathValue("action"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	ns, err := s.Engine.Notifications(r.Context(), r.URL.Query().Get("unread") == "true")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"notifications": ns})
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.MarkRead(r.Context(), r.PathValue("id")); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"read": r.PathValue("id")})
}

// L2 tools (ADR-0038): the owner reviews the full source, tests and benchmark,
// then approves the exact source hash, rejects, or disables.
func (s *Server) listL2(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Engine.ListL2(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tools": ts})
}

func (s *Server) getL2(w http.ResponseWriter, r *http.Request) {
	t, err := s.Engine.GetL2(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}

func (s *Server) l2Action(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		SHA256 string `json:"sha256"`
		Reason string `json:"reason"`
		Code   string `json:"code"` // web: step-up TOTP code
	}
	if r.ContentLength != 0 {
		if err := decodeStrict(r, &in); err != nil {
			s.mapErr(w, r, err)
			return
		}
	}
	var t any
	var err error
	switch r.PathValue("action") {
	case "approve":
		by := "owner"
		if viaWeb(r) {
			if !s.stepUp(r, in.Code) {
				s.fail(w, r, 401, "step_up_required", "enter a current code from your authenticator app to approve")
				return
			}
			by = "owner(web, step-up)"
		}
		t, err = s.Engine.DecideL2(r.Context(), id, true, in.SHA256, by)
	case "reject":
		t, err = s.Engine.DecideL2(r.Context(), id, false, "", "owner")
	case "disable":
		t, err = s.Engine.DisableL2(r.Context(), id, "owner", in.Reason)
	default:
		s.fail(w, r, 404, "not_found", "no such action")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, t)
}

// L3 brain variants (ADR-0048): listing, owner decision (bound to the sha256,
// step-up on the web), and rollback.
func (s *Server) listBrain(w http.ResponseWriter, r *http.Request) {
	vs, err := s.Engine.ListBrainVariants(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"variants": vs})
}

func (s *Server) getBrain(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.GetBrainVariant(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) brainAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		SHA256 string `json:"sha256"`
		Reason string `json:"reason"`
		Code   string `json:"code"`
	}
	if r.ContentLength != 0 {
		if err := decodeStrict(r, &in); err != nil {
			s.mapErr(w, r, err)
			return
		}
	}
	var v any
	var err error
	switch r.PathValue("action") {
	case "approve":
		by := "owner"
		if viaWeb(r) {
			if !s.stepUp(r, in.Code) {
				s.fail(w, r, 401, "step_up_required", "enter a current code from your authenticator app to approve")
				return
			}
			by = "owner(web, step-up)"
		}
		v, err = s.Engine.DecideBrain(r.Context(), id, true, in.SHA256, by)
	case "reject":
		v, err = s.Engine.DecideBrain(r.Context(), id, false, "", "owner")
	case "rollback":
		v, err = s.Engine.RollbackBrain(r.Context(), id, "owner", in.Reason)
	default:
		s.fail(w, r, 404, "not_found", "no such action")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

// browserCheck is an owner diagnostic of the browser stack (ADR-0039); no model.
func (s *Server) browserCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL          string   `json:"url"`
		AllowDomains []string `json:"allow_domains"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	out, err := s.Engine.BrowserCheck(r.Context(), in.URL, in.AllowDomains)
	if err != nil {
		if errors.Is(err, engine.ErrInvalid) {
			s.mapErr(w, r, err)
			return
		}
		s.fail(w, r, 502, "browser_failed", err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// Browser login profiles and one-time codes (ADR-0040). Secrets are never returned.
func (s *Server) browserProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"profiles": s.Engine.BrowserProfiles(r.Context())})
}

func (s *Server) importBrowserSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		State json.RawMessage `json:"state"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	n, err := s.Engine.ImportBrowserSession(r.Context(), r.PathValue("name"), in.State)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"profile": r.PathValue("name"), "cookies_kept": n})
}

func (s *Server) deleteBrowserSession(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.DeleteBrowserSession(r.Context(), r.PathValue("name")); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": r.PathValue("name")})
}

func (s *Server) browserCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if err := s.Engine.BrowserCode(r.Context(), r.PathValue("id"), in.Code); err != nil {
		if errors.Is(err, engine.ErrInvalid) || errors.Is(err, task.ErrInvalidTransition) {
			s.mapErr(w, r, err)
			return
		}
		s.fail(w, r, 502, "browser_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"entered": true}) // the code itself is not echoed
}

// Workspace handoff (ADR-0042): the owner takes over a browser session, sees
// it, acts on it and returns control; or signs in to a login profile by hand.
func (s *Server) browserTakeover(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.BrowserTakeover(r.Context(), r.PathValue("id")); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"session": r.PathValue("id"), "owner_control": true})
}

func (s *Server) browserSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"sessions": s.Engine.BrowserSessions()})
}

func (s *Server) browserScreen(w http.ResponseWriter, r *http.Request) {
	png, u, err := s.Engine.BrowserScreen(r.Context(), r.PathValue("key"))
	if err != nil {
		if errors.Is(err, engine.ErrNoBrowserSession) {
			s.mapErr(w, r, err)
			return
		}
		s.fail(w, r, 502, "browser_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Page-URL", u)
	w.Write(png)
}

func (s *Server) browserInput(w http.ResponseWriter, r *http.Request) {
	var in engine.OwnerInput
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	u, err := s.Engine.BrowserInput(r.Context(), r.PathValue("key"), in)
	if err != nil {
		if errors.Is(err, engine.ErrInvalid) || errors.Is(err, task.ErrInvalidTransition) || errors.Is(err, engine.ErrNoBrowserSession) {
			s.mapErr(w, r, err)
			return
		}
		s.fail(w, r, 502, "browser_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"url": u})
}

func (s *Server) browserReturn(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.BrowserReturn(r.Context(), r.PathValue("key")); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"owner_control": false})
}

func (s *Server) profileLoginStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL          string   `json:"url"`
		ExtraDomains []string `json:"extra_domains"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	key, err := s.Engine.StartProfileLogin(r.Context(), r.PathValue("name"), in.URL, in.ExtraDomains)
	if err != nil {
		if errors.Is(err, engine.ErrInvalid) {
			s.mapErr(w, r, err)
			return
		}
		s.fail(w, r, 502, "browser_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"session": key})
}

func (s *Server) profileLoginFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Save bool `json:"save"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	saved, err := s.Engine.FinishProfileLogin(r.Context(), r.PathValue("name"), in.Save)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"saved": saved})
}

// snoozeNotification hides a notification for N minutes (ODI A7).
func (s *Server) snoozeNotification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes int `json:"minutes"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if err := s.Engine.SnoozeNotification(r.Context(), r.PathValue("id"), in.Minutes); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"snoozed": r.PathValue("id"), "minutes": in.Minutes})
}

// ackNotification acknowledges a notification (ODI A7).
func (s *Server) ackNotification(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.AckNotification(r.Context(), r.PathValue("id")); err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"acknowledged": r.PathValue("id")})
}

// setMemoryExpiry sets or clears a memory's expiry (ODI A7).
func (s *Server) setMemoryExpiry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days int `json:"days"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	m, err := s.Engine.SetMemoryExpiry(r.Context(), r.PathValue("id"), in.Days)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, m)
}

// retentionReport is a dry run: what the owner's retention proposal would purge. Nothing is deleted (ODI A7, ADR-0076).
func (s *Server) retentionReport(w http.ResponseWriter, r *http.Request) {
	p, would, err := s.Engine.RetentionReport(r.Context(), s.Store.R)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	note := "report only: set a policy with PUT and enforce=true to delete; the audit log and task events are never part of a retention proposal"
	if p.Enforce {
		note = "enforced daily by the maintenance loop (and by POST /admin/retention/purge); the audit log, task events and referenced memories are never deleted"
	}
	writeJSON(w, 200, map[string]any{"policy": p, "would_purge": would, "enforced": p.Enforce, "floors": engine.RetentionFloors, "note": note})
}

// putRetention stores the owner's retention numbers (owner only; refuses numbers under the floors). With enforce=true the daily maintenance applies them.
func (s *Server) putRetention(w http.ResponseWriter, r *http.Request) {
	var p engine.RetentionPolicy
	if err := decodeStrict(r, &p); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if err := engine.ValidateRetention(p); err != nil {
		s.mapErr(w, r, err)
		return
	}
	b, _ := json.Marshal(p)
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `insert into settings(key, value, updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, engine.RetentionSetting, string(b), time.Now().UnixMilli()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "RETENTION_POLICY_SET", PrincipalOf(r).Name, "", requestID(r), map[string]any{"policy": p})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.retentionReport(w, r)
}

// purgeRetention runs the purge now. It needs {"confirm":"purge"}: deleting is never a side effect of a stray request.
func (s *Server) purgeRetention(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeStrict(r, &in); err != nil || in.Confirm != "purge" {
		s.fail(w, r, 400, "invalid_input", `send {"confirm":"purge"} to delete what the retention policy allows`)
		return
	}
	n, err := s.Engine.PurgeRetention(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if n == nil {
		s.fail(w, r, 409, "conflict", "the retention policy is not set or enforce is false: nothing was deleted")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}
