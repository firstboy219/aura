package api

import (
	"errors"
	"net/http"
	"strconv"

	"aura.local/core/internal/engine"
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
		Content string `json:"content"`
		Private bool   `json:"private"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	m, err := s.Engine.AddOwnerMemory(r.Context(), in.Content, in.Private)
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
		t, err = s.Engine.DecideL2(r.Context(), id, true, in.SHA256, "owner")
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
