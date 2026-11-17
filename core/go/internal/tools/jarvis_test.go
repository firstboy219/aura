package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ADR-0082: a worker uses the owner's JARVIS features with AURA's own service token; the answer is data, and a missing token or a refusal
// is a tool failure, never a silent success.
func TestJarvisFeatureCallsJarvisWithItsTokenAndRoutesOnlyItsTool(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/aura/fitur" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"fitur":"quran","aksi":"lihat","hasil":{"surah":"Al-Mulk"}}`))
	}))
	defer srv.Close()
	j := &JarvisFeatures{Base: srv.URL, Token: func(context.Context) (string, error) { return "tok-123", nil }}
	out, err := j.Run(context.Background(), map[string]any{"fitur": "quran", "aksi": "lihat", "data": map[string]any{"n": 67.0}})
	if err != nil || !strings.Contains(out, "Al-Mulk") || !strings.Contains(out, "not instructions") {
		t.Fatalf("%v %q", err, out)
	}
	if auth != "Bearer tok-123" || got["fitur"] != "quran" || got["aksi"] != "lihat" || got["data"].(map[string]any)["n"] != 67.0 {
		t.Fatalf("auth=%q body=%v", auth, got)
	}
	if out, _ := j.Run(context.Background(), map[string]any{"fitur": "musik"}); !strings.Contains(out, "musik.daftar") {
		t.Fatalf("no action means the feature's list: %q", out)
	}
	if _, err := j.Run(context.Background(), map[string]any{}); !errors.Is(err, ErrSearch) {
		t.Fatalf("a call without a feature must fail: %v", err)
	}
	noTok := &JarvisFeatures{Base: srv.URL, Token: func(context.Context) (string, error) { return "", nil }}
	if _, err := noTok.Run(context.Background(), map[string]any{"fitur": "quran"}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("no token must fail: %v", err)
	}
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer refused.Close()
	if _, err := (&JarvisFeatures{Base: refused.URL, Token: j.Token}).Run(context.Background(), map[string]any{"fitur": "quran"}); err == nil {
		t.Fatal("a refusal must be a failure")
	}
	other := 0
	w := &WithJarvis{Info: runFunc(func(ctx context.Context, tool string, args map[string]any) (string, error) {
		other++
		return "cuaca", nil
	}), Jarvis: j}
	if out, _ := w.Run(context.Background(), "info.weather", nil); out != "cuaca" || other != 1 {
		t.Fatal("other info tools still go to the info runner")
	}
	if out, _ := w.Run(context.Background(), JarvisTool, map[string]any{"fitur": "quran"}); !strings.Contains(out, "Al-Mulk") || other != 1 {
		t.Fatal("jarvis.fitur goes to JARVIS")
	}
}

type runFunc func(ctx context.Context, tool string, args map[string]any) (string, error)

func (f runFunc) Run(ctx context.Context, tool string, args map[string]any) (string, error) {
	return f(ctx, tool, args)
}
