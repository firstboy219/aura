package app

import (
	"testing"

	"aura.local/core/internal/egresslog"
)

func TestEgressListShowsHostsOnlyAndIsOwnerOnly(t *testing.T) {
	e := newWebEnv(t)
	owner := e.auth()
	resp, b := e.req("POST", "/api/v1/admin/members", `{"name":"eg-approver","role":"approver"}`, owner)
	if resp.StatusCode != 201 {
		t.Fatalf("%d", resp.StatusCode)
	}
	approver := map[string]string{"Authorization": "Bearer " + b["token"].(string)}
	egresslog.Default.Note("provider", "api.example-provider.test:443")
	egresslog.Default.Note("provider", "api.example-provider.test")

	if resp, _ := e.req("GET", "/api/v1/admin/egress", "", approver); resp.StatusCode != 403 {
		t.Fatalf("owner only: %d", resp.StatusCode)
	}
	resp, b = e.req("GET", "/api/v1/admin/egress", "", owner)
	if resp.StatusCode != 200 || b["mode"] != "observe" {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
	found := false
	for _, d := range b["destinations"].([]any) {
		m := d.(map[string]any)
		if m["host"] == "api.example-provider.test" && m["purpose"] == "provider" && m["count"].(float64) >= 2 {
			found = true
		}
		if m["host"] == "api.example-provider.test:443" {
			t.Fatal("a port must not be part of the host")
		}
	}
	if !found {
		t.Fatalf("destination missing: %v", b)
	}
}
