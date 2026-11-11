package api

import (
	"net/http"

	"aura.local/core/internal/engine"
)

// Designed screens (ADR-0060). The clients draw the home screen from the
// active design and the Core's context of the moment; a redesign is an owner
// task (POST /tasks with "design"), so it is planned, budgeted and audited
// like any other request.

func (s *Server) uiHome(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.ActiveScreen(r.Context(), "home")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"screen": v, "context": s.Engine.Home(r.Context())})
}

func (s *Server) uiScreen(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	v, err := s.Engine.ActiveScreen(r.Context(), name)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	vs, err := s.Engine.ScreenVersions(r.Context(), name)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"screen": v, "versions": vs, "default": engine.DefaultScreen(name)})
}

// uiScreenAction: rollback (the design before the active one), reset (the
// built-in default) or use (a stored version).
func (s *Server) uiScreenAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("name"), r.PathValue("action")
	var in struct {
		Version int `json:"version"`
	}
	if r.ContentLength != 0 {
		if err := decodeStrict(r, &in); err != nil {
			s.mapErr(w, r, err)
			return
		}
	}
	var v *engine.ScreenVersion
	var err error
	switch action {
	case "rollback":
		v, err = s.Engine.RollbackScreen(r.Context(), name)
	case "reset":
		v, err = s.Engine.UseScreen(r.Context(), name, 0)
	case "use":
		if in.Version < 1 {
			s.fail(w, r, 400, "invalid_input", "version must be 1 or more")
			return
		}
		v, err = s.Engine.UseScreen(r.Context(), name, in.Version)
	default:
		s.fail(w, r, 404, "not_found", "unknown action")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.audit(r, "UI_SCREEN_"+action, map[string]any{"screen": name, "version": v.Version})
	writeJSON(w, 200, map[string]any{"screen": v})
}
