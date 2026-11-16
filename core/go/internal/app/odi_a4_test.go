package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/vault"
)

func nexusEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, 1000, func(cfg *config.Config, dir string) {
		key := filepath.Join(dir, "master.key")
		if _, err := vault.GenerateKeyFile(key); err != nil {
			t.Fatal(err)
		}
		cfg.Secrets.MasterKeyFile = key
	})
}

func auditCount(e *env, typ string) int {
	var n int
	e.a.Store.R.QueryRow("select count(*) from audit_events where type=?", typ).Scan(&n)
	return n
}

func TestNexusAdminGateOwnerDisableAndAudit(t *testing.T) {
	e := nexusEnv(t)
	resp, m := e.req("GET", "/api/v1/admin/nexus", "", e.auth())
	pol, _ := m["policy"].(map[string]any)
	if resp.StatusCode != 200 || pol["mode"] != "off" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if resp, _ := e.req("GET", "/api/v1/admin/nexus", "", nil); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}
	// invalid input is refused
	for _, body := range []string{`{"mode":"sometimes"}`, `{"mode":"primary"}`, `{"unknown_field":1}`, `{"fallback_budget_usd":9999}`} {
		if resp, _ := e.req("PUT", "/api/v1/admin/nexus", body, e.auth()); resp.StatusCode != 400 {
			t.Errorf("%s: %d", body, resp.StatusCode)
		}
	}
	// primary is gated: an unreachable NEXUS cannot become primary and the policy is left as it was
	resp, m = e.req("PUT", "/api/v1/admin/nexus", `{"mode":"primary","base_url":"http://127.0.0.1:1"}`, e.auth())
	if resp.StatusCode != 409 {
		t.Fatalf("an unreachable NEXUS became primary: %d %v", resp.StatusCode, m)
	}
	if _, m = e.req("GET", "/api/v1/admin/nexus", "", e.auth()); m["policy"].(map[string]any)["mode"] != "off" {
		t.Fatalf("policy changed by a refused switch: %v", m["policy"])
	}
	// a healthy NEXUS can
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer fake.Close()
	resp, m = e.req("PUT", "/api/v1/admin/nexus", `{"mode":"primary","base_url":"`+fake.URL+`","fallback_providers":["gemini"]}`, e.auth())
	if resp.StatusCode != 200 || m["policy"].(map[string]any)["mode"] != "primary" || m["health"].(map[string]any)["state"] != "HEALTHY" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if auditCount(e, "NEXUS_POLICY_CHANGED") != 1 {
		t.Fatalf("policy change not audited exactly once: %d", auditCount(e, "NEXUS_POLICY_CHANGED"))
	}
	// owner disable / enable
	resp, m = e.req("POST", "/api/v1/admin/nexus/owner-disable", `{"minutes":30}`, e.auth())
	if resp.StatusCode != 200 || m["policy"].(map[string]any)["owner_disabled_until_ms"].(float64) <= 0 {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if resp, _ := e.req("POST", "/api/v1/admin/nexus/owner-disable", `{"minutes":5000}`, e.auth()); resp.StatusCode != 400 {
		t.Fatalf("minutes out of range: %d", resp.StatusCode)
	}
	resp, m = e.req("POST", "/api/v1/admin/nexus/owner-disable", `{"minutes":0}`, e.auth())
	if resp.StatusCode != 200 || m["policy"].(map[string]any)["owner_disabled_until_ms"].(float64) != 0 || auditCount(e, "NEXUS_OWNER_DISABLE") != 2 {
		t.Fatalf("%d %v audit=%d", resp.StatusCode, m, auditCount(e, "NEXUS_OWNER_DISABLE"))
	}
	// the verification chain still holds after all of it
	if resp, m := e.req("GET", "/api/v1/admin/audit/verify", "", e.auth()); resp.StatusCode != 200 || m["valid"] != true {
		t.Fatalf("audit chain: %d %v", resp.StatusCode, m)
	}
}

func TestTeacherGateEndpointNeedsItsOwnServiceToken(t *testing.T) {
	e := nexusEnv(t)
	body := `{"gap":"networking","question":"How does TCP slow start work?"}`
	// no token set in the vault: nothing can pass, not even the owner's API token
	for _, hdr := range []map[string]string{nil, e.auth(), {"Authorization": "Bearer " + strings.Repeat("x", 40)}} {
		if resp, _ := e.req("POST", "/api/v1/teacher/requests", body, hdr); resp.StatusCode != 401 {
			t.Fatalf("%v: %d", hdr, resp.StatusCode)
		}
	}
	if auditCount(e, "TEACHER_AUTH_FAILED") != 3 {
		t.Fatalf("auth failures audited: %d", auditCount(e, "TEACHER_AUTH_FAILED"))
	}
	tok := strings.Repeat("t", 40)
	if resp, _ := e.req("PUT", "/api/v1/admin/credentials/teacher_service_token", `{"value":"`+tok+`"}`, e.auth()); resp.StatusCode != 200 {
		t.Fatalf("store token: %d", resp.StatusCode)
	}
	hdr := map[string]string{"Authorization": "Bearer " + tok}
	// the right token reaches the gate, which refuses by default (the owner has not enabled it)
	resp, m := e.req("POST", "/api/v1/teacher/requests", body, hdr)
	errm, _ := m["error"].(map[string]any)
	if resp.StatusCode != 403 || errm["code"] != "TEACHER_DISABLED" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	// the owner's API token is not a Teacher token, and the Teacher token is not an owner token
	if resp, _ := e.req("GET", "/api/v1/admin/nexus", "", hdr); resp.StatusCode != 401 {
		t.Fatalf("the Teacher token opened an owner endpoint: %d", resp.StatusCode)
	}
	if resp, _ := e.req("POST", "/api/v1/teacher/requests", `{"question":"x","unexpected":1}`, hdr); resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d", resp.StatusCode)
	}
}
