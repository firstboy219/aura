package api

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// CognitionMessage is one chat message for NEXUS.
type CognitionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cognitionIn struct {
	Messages  []CognitionMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	Priority  string             `json:"priority"`
	DeadlineS int                `json:"deadline_s"`
}

// completeCognition: JARVIS's light work goes to NEXUS first (owner 2026-11-17: "yang ringan juga lewat NEXUS; AI model hanya jika NEXUS tidak
// bisa menghandle atau bermasalah"). NEXUS answers within the caller's deadline or the request is cancelled there and the caller is told so
// (503) — then, and only then, the caller may use an outside model. Never an outside model here.
func (s *Server) completeCognition(w http.ResponseWriter, r *http.Request) {
	if s.Cognition == nil {
		s.fail(w, r, 503, "nexus_unavailable", "NEXUS is not configured")
		return
	}
	var in cognitionIn
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if len(in.Messages) == 0 || len(in.Messages) > 40 {
		s.fail(w, r, 400, "invalid_input", "messages: 1..40")
		return
	}
	total := 0
	for _, m := range in.Messages {
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" {
			s.fail(w, r, 400, "invalid_input", "role: system|user|assistant")
			return
		}
		total += len(m.Content)
	}
	if total > 24000 {
		s.fail(w, r, 413, "too_large", "more than NEXUS reads in time")
		return
	}
	switch in.Priority {
	case "":
		in.Priority = "normal"
	case "interactive", "normal", "background":
	default:
		s.fail(w, r, 400, "invalid_input", "priority: interactive|normal|background")
		return
	}
	if in.MaxTokens <= 0 || in.MaxTokens > 1200 {
		in.MaxTokens = 300
	}
	deadline := time.Duration(in.DeadlineS) * time.Second
	if deadline < 5*time.Second || deadline > 10*time.Minute {
		deadline = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()
	t0 := time.Now()
	content, model, err := s.Cognition(ctx, in.Messages, in.MaxTokens, in.Priority)
	if err != nil {
		code := "nexus_unavailable"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = "deadline_exceeded"
		}
		s.fail(w, r, 503, code, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"content": content, "model": model, "ms": time.Since(t0).Milliseconds(), "authority": "nexus"})
}
