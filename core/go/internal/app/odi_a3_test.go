package app

import "testing"

func TestAuditVerifyEndpointAndRevokeOfUnknownApproval(t *testing.T) {
	e := newEnv(t, 100)
	resp, m := e.req("GET", "/api/v1/admin/audit/verify", "", e.auth())
	if resp.StatusCode != 200 || m["valid"] != true || m["head"] == "" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if resp, _ := e.req("GET", "/api/v1/admin/audit/verify", "", nil); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated verify: %d", resp.StatusCode)
	}
	if resp, _ := e.req("POST", "/api/v1/approvals/01JUNKNOWNUNKNOWNUNKNOWN00/revoke", `{"reason":"x"}`, e.auth()); resp.StatusCode != 404 {
		t.Fatalf("revoke of an unknown approval: %d", resp.StatusCode)
	}
}
