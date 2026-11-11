package api

import (
	"net/http"
	"strconv"
)

// Workspace (ADR-0061): the owner's notes, and the pictures the Core found
// for the figures of a reply.

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ns, err := s.Engine.Notes(r.Context(), limit)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"notes": ns})
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	n, err := s.Engine.AddNote(r.Context(), in.Text)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.audit(r, "NOTE_ADDED", map[string]any{"note_id": n.ID})
	writeJSON(w, 201, n)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text *string `json:"text"`
		Done *bool   `json:"done"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	n, err := s.Engine.UpdateNote(r.Context(), r.PathValue("id"), in.Text, in.Done)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.audit(r, "NOTE_UPDATED", map[string]any{"note_id": n.ID})
	writeJSON(w, 200, n)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.DeleteNote(r.Context(), id); err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.audit(r, "NOTE_DELETED", map[string]any{"note_id": id})
	writeJSON(w, 200, map[string]any{"deleted": id})
}

// shellLog lists AURA's server commands with the watcher's verdicts (ADR-0065).
func (s *Server) shellLog(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Engine.ShellLog(r.Context(), r.URL.Query().Get("task_id"), limit)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"commands": rows})
}

func (s *Server) listReminders(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Engine.Reminders(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"reminders": rs})
}

func (s *Server) deleteReminder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Engine.DeleteReminder(r.Context(), id); err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.audit(r, "REMINDER_DELETED", map[string]any{"reminder_id": id})
	writeJSON(w, 200, map[string]any{"deleted": id})
}

// media serves a stored picture. The type was decided from the bytes when it
// was stored; it is never sniffed or run as a document.
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	m, err := s.Engine.GetMedia(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.WriteHeader(200)
	w.Write(m.Bytes)
}
