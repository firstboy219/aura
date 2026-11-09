// Package obs holds logging and redaction (ADR-0008). Every log line and event
// payload passes through Redact so that loaded secrets never leave the Core.
package obs

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

var (
	mu      sync.RWMutex
	secrets []string
	bearer  = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	orKey   = regexp.MustCompile(`sk-or-[A-Za-z0-9_-]{8,}`)
)

// RegisterSecret adds a value that must never appear in logs or events.
func RegisterSecret(s string) {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return
	}
	mu.Lock()
	secrets = append(secrets, s)
	mu.Unlock()
}

// Redact replaces registered secrets, bearer tokens and OpenRouter-style keys.
func Redact(s string) string {
	mu.RLock()
	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			s = strings.ReplaceAll(s, sec, "[REDACTED]")
		}
	}
	mu.RUnlock()
	s = bearer.ReplaceAllString(s, "${1}[REDACTED]")
	return orKey.ReplaceAllString(s, "[REDACTED]")
}

// Handler wraps a slog.Handler and redacts message and string attributes.
type Handler struct{ slog.Handler }

func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a))
		return true
	})
	return h.Handler.Handle(ctx, nr)
}

func (h Handler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = redactAttr(a)
	}
	return Handler{h.Handler.WithAttrs(out)}
}

func (h Handler) WithGroup(name string) slog.Handler { return Handler{h.Handler.WithGroup(name)} }

func redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, Redact(err.Error()))
		}
	}
	return a
}
