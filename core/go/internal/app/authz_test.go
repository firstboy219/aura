package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// D.1 P5 Workspace Admin foundation: roles are enforced server-side on every
// route; a member cannot do what their role does not allow, whatever the UI shows.
func TestRoleAuthorization(t *testing.T) {
	w := newWebEnv(t) // vault configured
	owner := map[string]string{"Authorization": "Bearer " + w.token}
	tokens := map[string]string{}
	for _, role := range []string{"viewer", "operator", "approver"} {
		resp, body := w.req("POST", "/api/v1/admin/members", `{"name":"m-`+role+`","role":"`+role+`"}`, owner)
		if resp.StatusCode != 201 {
			t.Fatalf("create %s: %d %v", role, resp.StatusCode, body)
		}
		tokens[role] = body["token"].(string)
	}
	as := func(role string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + tokens[role]}
	}
	task := `{"objective":"Fetch https://example.com/ and write reports/a.md","tools":["web.fetch","workspace.write"],"allow_domains":["example.com"]}`
	approve := `{"action_hash":"` + strings.Repeat("b", 64) + `"}`
	for _, c := range []struct {
		role, method, path, body string
		want                     int
	}{
		{"viewer", "GET", "/api/v1/tasks", "", 200},
		{"viewer", "POST", "/api/v1/tasks", task, 403},
		{"viewer", "GET", "/api/v1/admin/members", "", 403},
		{"operator", "POST", "/api/v1/tasks", task, 201},
		{"operator", "POST", "/api/v1/approvals/01APPROVALAAAAAAAAAAAAAAAA/approve", approve, 403},
		{"operator", "POST", "/api/v1/system/stop", "{}", 403},
		{"operator", "GET", "/api/v1/browser/sessions", "", 403},
		{"approver", "POST", "/api/v1/approvals/01APPROVALAAAAAAAAAAAAAAAA/approve", approve, 404}, // allowed; the approval does not exist
		{"approver", "POST", "/api/v1/tools/01TOOLAAAAAAAAAAAAAAAAAAAA/approve", `{"sha256":"x"}`, 403},
		{"approver", "PUT", "/api/v1/admin/credentials/x", `{"value":"v"}`, 403},
		{"approver", "DELETE", "/api/v1/memories/01MEMAAAAAAAAAAAAAAAAAAAAA", "", 403},
	} {
		resp, body := w.req(c.method, c.path, c.body, as(c.role))
		if resp.StatusCode != c.want {
			t.Fatalf("%s %s %s: %d, want %d (%v)", c.role, c.method, c.path, resp.StatusCode, c.want, body)
		}
	}
	var denied int
	w.a.Store.R.QueryRow("select count(*) from audit_events where type='AUTHZ_DENIED'").Scan(&denied)
	if denied < 7 {
		t.Fatalf("denials audited: %d", denied)
	}
	// A disabled member is refused entirely.
	_, list := w.req("GET", "/api/v1/admin/members", "", owner)
	var viewerID string
	for _, m := range list["members"].([]any) {
		if mm := m.(map[string]any); mm["name"] == "m-viewer" {
			viewerID = mm["id"].(string)
		}
	}
	w.req("POST", "/api/v1/admin/members/"+viewerID+"/disable", "", owner)
	if resp, _ := w.req("GET", "/api/v1/tasks", "", as("viewer")); resp.StatusCode != 401 {
		t.Fatalf("disabled member: %d", resp.StatusCode)
	}
}

// Connector credentials are write-only; the budget is an owner setting.
func TestAdminCredentialsAndBudget(t *testing.T) {
	w := newWebEnv(t)
	owner := map[string]string{"Authorization": "Bearer " + w.token}
	if resp, _ := w.req("PUT", "/api/v1/admin/credentials/telegram", `{"value":"123456:secret-bot-token"}`, owner); resp.StatusCode != 200 {
		t.Fatalf("put credential: %d", resp.StatusCode)
	}
	resp, body := w.req("GET", "/api/v1/admin/credentials", "", owner)
	b, _ := json.Marshal(body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), "telegram") || strings.Contains(string(b), "secret-bot-token") {
		t.Fatalf("list credentials: %d %s", resp.StatusCode, b)
	}
	if resp, _ := w.req("GET", "/api/v1/admin/credentials/telegram", "", owner); resp.StatusCode != 404 && resp.StatusCode != 405 {
		t.Fatalf("a credential value is readable: %d", resp.StatusCode)
	}
	if resp, _ := w.req("PUT", "/api/v1/admin/budget", `{"daily_budget_usd":2.5}`, owner); resp.StatusCode != 200 || w.a.Governor.Limit() != 2.5 {
		t.Fatalf("budget: %d limit %v", resp.StatusCode, w.a.Governor.Limit())
	}
	var v string
	w.a.Store.R.QueryRow("select value from settings where key='daily_budget_usd'").Scan(&v)
	if v != "2.5" {
		t.Fatalf("budget not persisted: %q", v)
	}
}
