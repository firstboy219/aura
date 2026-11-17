package app

import "testing"

func TestRetentionRoutesAreOwnerOnlyValidatedAndNeedConfirmation(t *testing.T) {
	e := newWebEnv(t)
	owner := e.auth()
	resp, b := e.req("POST", "/api/v1/admin/members", `{"name":"ret-approver","role":"approver"}`, owner)
	if resp.StatusCode != 201 {
		t.Fatalf("%d", resp.StatusCode)
	}
	approver := map[string]string{"Authorization": "Bearer " + b["token"].(string)}

	if resp, _ := e.req("PUT", "/api/v1/admin/retention", `{"read_notifications_days":30}`, approver); resp.StatusCode != 403 {
		t.Fatalf("only the owner sets retention: %d", resp.StatusCode)
	}
	if resp, _ := e.req("POST", "/api/v1/admin/retention/purge", `{"confirm":"purge"}`, approver); resp.StatusCode != 403 {
		t.Fatalf("only the owner purges: %d", resp.StatusCode)
	}
	if resp, _ := e.req("PUT", "/api/v1/admin/retention", `{"read_notifications_days":1,"enforce":true}`, owner); resp.StatusCode != 400 {
		t.Fatalf("a number under the floor is refused: %d", resp.StatusCode)
	}
	if resp, _ := e.req("PUT", "/api/v1/admin/retention", `{"read_notifications_days":30,"bogus":1}`, owner); resp.StatusCode != 400 {
		t.Fatalf("unknown fields are refused: %d", resp.StatusCode)
	}
	if resp, _ := e.req("POST", "/api/v1/admin/retention/purge", `{"confirm":"purge"}`, owner); resp.StatusCode != 409 {
		t.Fatalf("no policy: nothing was deleted and the answer says so: %d", resp.StatusCode)
	}
	resp, b = e.req("PUT", "/api/v1/admin/retention", `{"read_notifications_days":30,"shell_log_days":30,"retired_memories_days":90,"consumed_approvals_days":90,"enforce":true}`, owner)
	if resp.StatusCode != 200 || b["enforced"] != true {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
	if resp, _ := e.req("POST", "/api/v1/admin/retention/purge", `{}`, owner); resp.StatusCode != 400 {
		t.Fatalf("deleting needs the explicit confirmation: %d", resp.StatusCode)
	}
	resp, b = e.req("POST", "/api/v1/admin/retention/purge", `{"confirm":"purge"}`, owner)
	if resp.StatusCode != 200 || b["deleted"] == nil {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
	var n int
	e.a.Store.R.QueryRow("select count(*) from audit_events where type='RETENTION_POLICY_SET'").Scan(&n)
	if n != 1 {
		t.Fatalf("the policy change is audited: %d", n)
	}
}
