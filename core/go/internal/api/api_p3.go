package api

import (
	"net/http"
	"strconv"
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
