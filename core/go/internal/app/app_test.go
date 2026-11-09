package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

type env struct {
	t     *testing.T
	a     *App
	srv   *httptest.Server
	token string
}

func newEnv(t *testing.T, rate int) *env {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := config.Parse([]byte("config_version: 1\n"), func(string) string { return "" })
	cfg.Server.TokenFile = filepath.Join(dir, "api_token")
	cfg.Server.RatePerS = rate
	cfg.Database.Path = filepath.Join(dir, "aura.db")
	cfg.Database.BackupDir = filepath.Join(dir, "backups")
	cfg.Workspace.Root = filepath.Join(dir, "ws")
	cfg.Worker.ScratchRoot = filepath.Join(dir, "scratch")
	cfg.Models = map[string][]config.ModelRoute{"standard": {{DisplayName: "S", APIModelID: "s", RouteID: "fake/s",
		PriceInPerMTok: 1, PriceOutPerMTok: 2, MaxOutputTokens: 1000}}}
	fake := &gatewaytest.Fake{Respond: func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
		return gatewaytest.Reply{Err: &gateway.ProviderError{Status: 400, Msg: "not used"}}
	}}
	a, err := Build(context.Background(), cfg, Options{Provider: fake, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.API.Handler())
	t.Cleanup(func() { srv.Close(); a.Worker.Close(); a.Close() })
	tok, err := config.ReadSecret(cfg.Server.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, a: a, srv: srv, token: tok}
}

func (e *env) req(method, path, body string, hdr map[string]string) (*http.Response, map[string]any) {
	r, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp, m
}

func (e *env) auth() map[string]string {
	return map[string]string{"Authorization": "Bearer " + e.token}
}

func TestTokenFileCreatedWithMode0400(t *testing.T) {
	e := newEnv(t, 20)
	fi, err := os.Stat(e.a.Cfg.Server.TokenFile)
	if err != nil || fi.Mode().Perm() != 0o400 || len(e.token) < 40 {
		t.Fatalf("%v %v", err, fi.Mode())
	}
}

func TestAuthRequiredEverywhereExceptHealth(t *testing.T) {
	e := newEnv(t, 100)
	resp, m := e.req("GET", "/api/v1/system/health", "", nil)
	if resp.StatusCode != 200 || m["live"] != true || m["ready"] != true {
		t.Fatalf("health %d %v", resp.StatusCode, m)
	}
	for _, ep := range [][2]string{{"POST", "/api/v1/tasks"}, {"GET", "/api/v1/tasks"}, {"GET", "/api/v1/tasks/X"},
		{"GET", "/api/v1/tasks/X/events"}, {"POST", "/api/v1/tasks/X/cancel"}, {"GET", "/api/v1/approvals"},
		{"POST", "/api/v1/approvals/X/approve"}, {"GET", "/api/v1/system/resources"}, {"POST", "/api/v1/system/stop"}} {
		for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong"}, {"Authorization": e.token}} {
			resp, m := e.req(ep[0], ep[1], "{}", hdr)
			errObj, _ := m["error"].(map[string]any)
			if resp.StatusCode != 401 || errObj["code"] != "unauthorized" || errObj["request_id"] == "" {
				t.Fatalf("%v with %v: %d %v", ep, hdr, resp.StatusCode, m)
			}
		}
	}
	var n int
	e.a.Store.R.QueryRow("select count(*) from audit_events where type='AUTH_FAILED'").Scan(&n)
	if n != 27 {
		t.Fatalf("auth failures audited: %d", n)
	}
	if resp, _ := e.req("GET", "/api/v1/system/resources", "", e.auth()); resp.StatusCode != 200 || resp.Header.Get("X-Request-Id") == "" {
		t.Fatal("authorized request failed")
	}
}

func TestCreateValidationIdempotencyAndErrors(t *testing.T) {
	e := newEnv(t, 100)
	h := e.auth()
	for _, body := range []string{`{}`, `{"objective":""}`, `{"objective":"x","tools":["shell"]}`, `{"objective":"x","extra":1}`,
		`{"objective":"x","allow_domains":["10.0.0.1"]}`, `not json`} {
		resp, m := e.req("POST", "/api/v1/tasks", body, h)
		if resp.StatusCode != 400 || m["error"].(map[string]any)["code"] != "invalid_input" {
			t.Fatalf("%s: %d %v", body, resp.StatusCode, m)
		}
	}
	big := `{"objective":"` + strings.Repeat("a", 70<<10) + `"}`
	if resp, _ := e.req("POST", "/api/v1/tasks", big, h); resp.StatusCode != 400 {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}
	h2 := map[string]string{"Authorization": h["Authorization"], "Idempotency-Key": "abc"}
	r1, t1 := e.req("POST", "/api/v1/tasks", `{"objective":"do x","allow_domains":["example.com"]}`, h2)
	r2, t2 := e.req("POST", "/api/v1/tasks", `{"objective":"do x","allow_domains":["example.com"]}`, h2)
	if r1.StatusCode != 201 || r2.StatusCode != 200 || t1["id"] != t2["id"] {
		t.Fatalf("idempotency: %d %d %v %v", r1.StatusCode, r2.StatusCode, t1["id"], t2["id"])
	}
	id := t1["id"].(string)
	if resp, _ := e.req("GET", "/api/v1/tasks/"+id, "", h); resp.StatusCode != 200 {
		t.Fatal("get task")
	}
	if resp, m := e.req("GET", "/api/v1/tasks/NOPE", "", h); resp.StatusCode != 404 || m["error"].(map[string]any)["code"] != "not_found" {
		t.Fatal("404")
	}
	e.req("POST", "/api/v1/tasks/"+id+"/cancel", "", h)
	if resp, m := e.req("POST", "/api/v1/tasks/"+id+"/resume", "", h); resp.StatusCode != 409 {
		t.Fatalf("resume cancelled: %d %v", resp.StatusCode, m)
	}
	if resp, _ := e.req("POST", "/api/v1/approvals/NOPE/approve", `{"action_hash":"`+strings.Repeat("a", 64)+`"}`, h); resp.StatusCode != 404 {
		t.Fatal("approve unknown")
	}
	if resp, _ := e.req("GET", "/api/v1/shell", "", h); resp.StatusCode != 404 {
		t.Fatal("unknown endpoint")
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, 2)
	got429 := false
	for i := 0; i < 10; i++ {
		resp, m := e.req("GET", "/api/v1/tasks", "", e.auth())
		if resp.StatusCode == 429 {
			got429 = m["error"].(map[string]any)["code"] == "rate_limited"
			break
		}
	}
	if !got429 {
		t.Fatal("rate limit not enforced")
	}
}

func TestKillSwitchEndpointAndHealth(t *testing.T) {
	e := newEnv(t, 100)
	resp, m := e.req("POST", "/api/v1/system/stop", "", e.auth())
	if resp.StatusCode != 202 {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	resp, m = e.req("GET", "/api/v1/system/health", "", nil)
	if resp.StatusCode != 503 || m["checks"].(map[string]any)["kill_switch"] != "active" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
}

// Responses conform to the OpenAPI component schemas.
func TestOpenAPIConformance(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(doc)
	inst, _ := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	c := jsonschema.NewCompiler()
	c.AddResource("mem://openapi.json", inst)
	schema := func(name string) *jsonschema.Schema {
		s, err := c.Compile("mem://openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	check := func(name string, v any) {
		b, _ := json.Marshal(v)
		i, _ := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err := schema(name).Validate(i); err != nil {
			t.Errorf("%s: %v\n%s", name, err, b)
		}
	}
	e := newEnv(t, 100)
	_, tk := e.req("POST", "/api/v1/tasks", `{"objective":"x","allow_domains":["example.com"]}`, e.auth())
	check("Task", tk)
	_, hl := e.req("GET", "/api/v1/system/health", "", nil)
	check("Health", hl)
	_, er := e.req("GET", "/api/v1/tasks/NOPE", "", e.auth())
	check("Error", er)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, evs := e.req("GET", fmt.Sprintf("/api/v1/tasks/%s/events", tk["id"]), "", e.auth())
		if list, _ := evs["events"].([]any); len(list) > 0 {
			for _, ev := range list {
				check("TaskEvent", ev)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no events")
}
