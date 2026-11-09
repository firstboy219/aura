package obs

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	RegisterSecret("super-secret-value-123")
	in := "key=super-secret-value-123 Authorization: Bearer abcdefghijklmnop sk-or-v1-0123456789abcdef"
	out := Redact(in)
	for _, leak := range []string{"super-secret-value-123", "abcdefghijklmnop", "sk-or-v1-0123456789abcdef"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leaked %q in %q", leak, out)
		}
	}
}

func TestHandlerRedactsAttrsAndErrors(t *testing.T) {
	RegisterSecret("another-secret-xyz")
	var buf bytes.Buffer
	log := slog.New(Handler{slog.NewJSONHandler(&buf, nil)})
	log.Info("msg another-secret-xyz", "k", "another-secret-xyz", "err", errors.New("boom another-secret-xyz"))
	if strings.Contains(buf.String(), "another-secret-xyz") {
		t.Fatal(buf.String())
	}
}
