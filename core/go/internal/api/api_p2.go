package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"aura.local/core/internal/tools"
)

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	tl, err := s.Engine.TaskTimeline(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"task": toAPI(tl.Task), "next_step": tl.NextStep, "completed": tl.Completed,
		"blockers": tl.Blockers, "pending_decisions": tl.Decisions, "spend": tl.Spend, "recent_events": tl.Recent})
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	fl, err := s.Engine.Files(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"files": fl})
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	content, err := s.Engine.ReadFile(r.Context(), r.PathValue("id"), r.PathValue("path"))
	switch {
	case errors.Is(err, tools.ErrBadPath), errors.Is(err, tools.ErrSymlink):
		s.fail(w, r, 400, "invalid_input", "invalid path")
		return
	case errors.Is(err, fs.ErrNotExist):
		s.fail(w, r, 404, "not_found", "no such file")
		return
	case err != nil:
		s.mapErr(w, r, err)
		return
	}
	if ct, ok := documentTypes[strings.ToLower(path.Ext(r.PathValue("path")))]; ok {
		// Documents and pictures are downloaded as files, never shown on the app's origin (ADR-0064).
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.Map(safeName, path.Base(r.PathValue("path")))+`"`)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Workspace files can come from the web (downloads): never active content on the app's origin.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Write([]byte(content))
}

var documentTypes = map[string]string{
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".pdf":  "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
}

// safeName keeps a download file name to letters, digits and . _ -.
func safeName(r rune) rune {
	if r == '.' || r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
		return r
	}
	return '_'
}

// stream is Server-Sent Events of task events in ULID order (ADR-0022),
// resumable with Last-Event-ID. Data is the same as /tasks/{id}/events.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{}) // a stream outlives the server's write timeout
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	rc.Flush()
	taskID := r.URL.Query().Get("task_id")
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = r.URL.Query().Get("after")
	}
	if last == "" { // start from now
		s.Store.R.QueryRowContext(r.Context(), "select coalesce(max(id),'') from task_events").Scan(&last)
	}
	ctx := r.Context()
	poll := time.NewTicker(300 * time.Millisecond)
	keep := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer keep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keep.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			if rc.Flush() != nil {
				return
			}
		case <-poll.C:
			n, newLast, err := s.emit(ctx, w, taskID, last)
			if err != nil {
				return
			}
			last = newLast
			if n > 0 && rc.Flush() != nil {
				return
			}
		}
	}
}

func (s *Server) emit(ctx context.Context, w http.ResponseWriter, taskID, after string) (int, string, error) {
	q := "select id, task_id, seq, type, step_id, actor, data_json, at from task_events where id > ? and visibility='timeline'"
	args := []any{after}
	if taskID != "" {
		q += " and task_id = ?"
		args = append(args, taskID)
	}
	rows, err := s.Store.R.QueryContext(ctx, q+" order by id limit 200", args...)
	if err != nil {
		return 0, after, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, tid, typ, actor, data string
		var seq, at int64
		var step *string
		rows.Scan(&id, &tid, &seq, &typ, &step, &actor, &data, &at)
		b, _ := json.Marshal(map[string]any{"task_id": tid, "seq": seq, "type": typ, "step_id": step, "actor": actor, "at": ts(at),
			"data": json.RawMessage(data)})
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, strings.ReplaceAll(typ, "\n", ""), b); err != nil {
			return n, after, err
		}
		after = id
		n++
	}
	return n, after, rows.Err()
}
