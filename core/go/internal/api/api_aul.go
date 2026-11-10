package api

import (
	"errors"
	"net/http"

	"aura.local/core/internal/engine"
	"aura.local/core/internal/task"
)

// AUL workflows (Blueprint D.1 P8, ADR-0050): check, register (owner), list,
// run (operator: the grant is exactly what the owner-registered workflow
// declares), disable (owner).

func (s *Server) aulCheck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	p, diags := s.Engine.CheckWorkflow(in.Source)
	if diags != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "diagnostics": diags})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "name": p.Name, "version": p.Version, "requires": p.Requires, "inputs": p.Inputs})
}

func (s *Server) registerWorkflow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	wf, diags, err := s.Engine.RegisterWorkflow(r.Context(), in.Source, PrincipalOf(r).ID)
	if diags != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "invalid_workflow", "message": "the workflow does not compile",
			"request_id": requestID(r)}, "diagnostics": diags})
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 201, wf)
}

func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Engine.ListWorkflows(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workflows": ws})
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	wf, err := s.Engine.GetWorkflow(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, wf)
}

func (s *Server) runWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.AcceptTasks != nil {
		if code := s.AcceptTasks(); code != "" {
			s.fail(w, r, 503, code, "not accepting new tasks: "+code)
			return
		}
	}
	var in struct {
		Inputs map[string]any `json:"inputs"`
		Budget *struct {
			MaxCostUSD     float64 `json:"max_cost_usd"`
			MaxIterations  int     `json:"max_iterations"`
			MaxWallSeconds int     `json:"max_wall_seconds"`
		} `json:"budget"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	var b task.Budget
	if in.Budget != nil {
		b = task.Budget{MaxCostUSD: in.Budget.MaxCostUSD, MaxIterations: in.Budget.MaxIterations, MaxWallSeconds: in.Budget.MaxWallSeconds}
	}
	t, err := s.Engine.RunWorkflow(r.Context(), r.PathValue("id"), in.Inputs, b)
	if errors.Is(err, engine.ErrAULUnavailable) {
		s.fail(w, r, 503, "aul_unavailable", "the AUL VM module is not installed")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.attribute(r, t.ID)
	writeJSON(w, 201, toAPI(t))
}

func (s *Server) disableWorkflow(w http.ResponseWriter, r *http.Request) {
	wf, err := s.Engine.DisableWorkflow(r.Context(), r.PathValue("id"), PrincipalOf(r).ID)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, wf)
}
