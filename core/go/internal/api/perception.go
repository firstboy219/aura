package api

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"

	"aura.local/core/internal/perception"
)

type perceptionIn struct {
	Modality   string   `json:"modality"`
	MediaB64   string   `json:"media_b64"`
	Question   string   `json:"question"`
	ConsentRef string   `json:"consent_ref"`
	Locale     []string `json:"locale"`
}

// createPerception: JARVIS (or the owner) hands AURA a piece of media and a question; AURA has AMPI perceive it and NEXUS reason over the evidence (ODI end to end). 202 + id; poll GET.
func (s *Server) createPerception(w http.ResponseWriter, r *http.Request) {
	if s.Perception == nil {
		s.fail(w, r, 404, "not_found", "the perception path is not configured")
		return
	}
	var in perceptionIn
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	media, err := base64.StdEncoding.DecodeString(in.MediaB64)
	if err != nil {
		s.fail(w, r, 400, "invalid_input", "media_b64 is not base64")
		return
	}
	if len(in.Locale) > 4 {
		in.Locale = in.Locale[:4]
	}
	id, err := s.Perception.Start(r.Context(), perception.Request{Modality: in.Modality, Question: in.Question, ConsentRef: in.ConsentRef, Locale: in.Locale, Media: media, By: PrincipalOf(r).Name})
	if err != nil {
		if errors.Is(err, perception.ErrInvalid) {
			s.fail(w, r, 400, "invalid_input", err.Error())
			return
		}
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"id": id, "state": "received", "poll": "/api/v1/perception/requests/" + id})
}

func (s *Server) getPerception(w http.ResponseWriter, r *http.Request) {
	if s.Perception == nil {
		s.fail(w, r, 404, "not_found", "the perception path is not configured")
		return
	}
	out, err := s.Perception.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		s.fail(w, r, 404, "not_found", "no such perception request")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
