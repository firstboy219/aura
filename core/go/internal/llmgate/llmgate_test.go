package llmgate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ADR-0083: JARVIS's provider requests go out only through AURA, with AURA's keys, on an allowlist; the answer streams back; every call is
// recorded without its content.
func TestGateForwardsAllowedCallsWithAurasKeyAndRecordsThem(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%d\"}}]}\n\n", i)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"cost\":0.00042}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	secrets := map[string]string{"vault:k-default": "key-default", "vault:k-chat": "key-chat"}
	var calls []Call
	g := &Gate{Upstreams: map[string]Upstream{
		"openrouter": {Base: up.URL + "/api/v1", Scheme: "Bearer", Keys: map[string]string{"": "vault:k-default", "chat": "vault:k-chat", "latar": "vault:missing"},
			Paths: []string{"chat/completions", "audio/transcriptions"}},
	}, Secret: func(ctx context.Context, ref string) (string, error) { return secrets[ref], nil },
		Record: func(ctx context.Context, c Call) { calls = append(calls, c) }}
	srv := httptest.NewServer(g)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/llm/openrouter/chat/completions", strings.NewReader(`{"model":"x/y:free","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Key-Class", "chat")
	req.Header.Set("X-Caller", "chat")
	req.Header.Set("Authorization", "Bearer jarvis-token-for-aura") // AURA's own auth, never forwarded
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"content":"2"`) || !strings.Contains(string(b), "[DONE]") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if gotAuth != "Bearer key-chat" || gotPath != "/api/v1/chat/completions" || !strings.Contains(gotBody, "x/y:free") {
		t.Fatalf("upstream got auth=%q path=%q body=%q", gotAuth, gotPath, gotBody)
	}
	if len(calls) != 1 || calls[0].Model != "x/y:free" || calls[0].Caller != "chat" || calls[0].Outcome != "ok" || calls[0].CostUSD != 0.00042 {
		t.Fatalf("recorded %+v", calls)
	}

	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/llm/openrouter/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Key-Class", "latar") // no key stored for this class: the provider's default key
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if gotAuth != "Bearer key-default" {
		t.Fatalf("class without a key must use the default: %q", gotAuth)
	}
	for _, bad := range []string{"/api/v1/llm/openrouter/admin/keys", "/api/v1/llm/evil/chat/completions", "/api/v1/llm/openrouter/chat/completions/../../x"} {
		resp, _ := http.Post(srv.URL+bad, "application/json", strings.NewReader("{}"))
		resp.Body.Close()
		if resp.StatusCode != 403 && resp.StatusCode != 301 && resp.StatusCode != 404 {
			t.Fatalf("%s must be refused: %d", bad, resp.StatusCode)
		}
	}
	g.Secret = func(ctx context.Context, ref string) (string, error) { return "", nil }
	resp, _ = http.Post(srv.URL+"/api/v1/llm/openrouter/chat/completions", "application/json", strings.NewReader("{}"))
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("no key → 503, got %d", resp.StatusCode)
	}
}

func TestDefaultUpstreamsAreHTTPSAndNarrow(t *testing.T) {
	for name, u := range Default() {
		if !strings.HasPrefix(u.Base, "https://") || len(u.Paths) == 0 || u.Keys[""] == "" {
			t.Fatalf("%s: %+v", name, u)
		}
		if allowed(u, "../secrets") || allowed(u, "") {
			t.Fatalf("%s allows a bad path", name)
		}
	}
}

// ADR-0084: once JARVIS's month is spent, paid OpenRouter calls get a 402 (JARVIS then uses free models); free ones still pass.
func TestBudgetStopsPaidCallsOnlyWhenTheMonthIsSpent(t *testing.T) {
	hits := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; fmt.Fprint(w, `{"choices":[]}`) }))
	defer up.Close()
	var calls []Call
	spent := 19.0
	g := &Gate{Upstreams: map[string]Upstream{"openrouter": {Base: up.URL, Scheme: "Bearer", Keys: map[string]string{"": "k"}, Paths: []string{"chat/completions"}}},
		Secret: func(ctx context.Context, ref string) (string, error) { return "key", nil },
		Record: func(ctx context.Context, c Call) { calls = append(calls, c) },
		Budget: func(ctx context.Context) (float64, float64) { return 20, spent }}
	srv := httptest.NewServer(g)
	defer srv.Close()
	post := func(model string) int {
		resp, err := http.Post(srv.URL+"/api/v1/llm/openrouter/chat/completions", "application/json", strings.NewReader(`{"model":"`+model+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if post("deepseek/deepseek-v4-pro") != 200 || hits != 1 {
		t.Fatal("under the cap a paid call goes out")
	}
	spent = 20
	if post("deepseek/deepseek-v4-pro") != 402 || hits != 1 || calls[len(calls)-1].Outcome != "budget" {
		t.Fatalf("over the cap a paid call is refused before it goes out: hits=%d calls=%+v", hits, calls)
	}
	if post("nvidia/nemotron-3-ultra-550b-a55b:free") != 200 || hits != 2 {
		t.Fatal("a free model still passes over the cap")
	}
	if paid("groq", "chat/completions", "openai/gpt-oss-20b") || paid("openrouter", "credits", "") || !paid("openrouter", "audio/speech", "openai/gpt-4o-mini-tts") {
		t.Fatal("paid() misjudges")
	}
}
