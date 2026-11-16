package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"aura.local/core/internal/odi"
)

// Self-engineering change proposals (ODI A8, ADR-0077). AURA records, classifies and gates a change; deployment itself stays with the existing release tooling.

func (s *Server) changeActor(r *http.Request, stepUpCode string) odi.Actor {
	p := PrincipalOf(r)
	a := odi.Actor{Name: p.Name, Owner: p.Role == RoleOwner}
	if a.Owner && viaWeb(r) && stepUpCode != "" && s.stepUp(r, stepUpCode) {
		a.StepUp = true
	}
	return a
}

func (s *Server) changeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, odi.ErrForbidden):
		s.fail(w, r, 403, "change_forbidden", err.Error())
	case errors.Is(err, odi.ErrChange):
		s.fail(w, r, 422, "change_invalid", err.Error())
	default:
		s.mapErr(w, r, err)
	}
}

func (s *Server) createChange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		odi.Proposal
		Diff       string `json:"diff"`
		ProposedBy string `json:"proposed_by"` // "agent:<name>" when an automated proposer files it
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 400<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		s.fail(w, r, 400, "invalid_input", "body must be a change proposal: "+err.Error())
		return
	}
	a := s.changeActor(r, "")
	if in.ProposedBy != "" {
		if !strings.HasPrefix(in.ProposedBy, "agent:") || len(in.ProposedBy) > 60 {
			s.fail(w, r, 400, "invalid_input", "proposed_by must look like agent:<name>")
			return
		}
		a = odi.Actor{Name: in.ProposedBy}
	}
	ch, err := s.Changes.Create(r.Context(), a, in.Proposal, in.Diff)
	if err != nil {
		s.changeErr(w, r, err)
		return
	}
	writeJSON(w, 201, ch)
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Changes.List(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"changes": cs})
}

func (s *Server) getChange(w http.ResponseWriter, r *http.Request) {
	ch, err := s.Changes.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, 404, "not_found", "no such change")
		return
	}
	writeJSON(w, 200, ch)
}

// decideChange approves or rejects. On the web a fresh TOTP code ("code") is the second factor L3 (and agent-proposed L2) need.
func (s *Server) decideChange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
		Code    string `json:"code"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	ch, err := s.Changes.Decide(r.Context(), r.PathValue("id"), s.changeActor(r, in.Code), in.Approve, truncateStr(in.Note, 500))
	if err != nil {
		s.changeErr(w, r, err)
		return
	}
	writeJSON(w, 200, ch)
}

func (s *Server) moveChange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To       string     `json:"to"`
		Note     string     `json:"note"`
		Evidence []odi.Evid `json:"evidence"`
		Code     string     `json:"code"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	ch, err := s.Changes.Move(r.Context(), r.PathValue("id"), s.changeActor(r, in.Code), in.To, truncateStr(in.Note, 500), in.Evidence)
	if err != nil {
		s.changeErr(w, r, err)
		return
	}
	writeJSON(w, 200, ch)
}
