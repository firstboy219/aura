package app

import (
	"strings"
	"testing"
)

// ADR-0053: the Android client signs in with username and password and gets a
// device token; wrong passwords lock the account; the role caps what the token
// can do; logout and account changes revoke tokens.
func TestAppAccountLogin(t *testing.T) {
	e := newEnv(t, 1000)
	owner := e.auth()
	if resp, _ := e.req("PUT", "/api/v1/admin/app-accounts/admin", `{"password":"short","role":"approver"}`, owner); resp.StatusCode != 400 {
		t.Fatalf("short password accepted: %d", resp.StatusCode)
	}
	if resp, _ := e.req("PUT", "/api/v1/admin/app-accounts/admin", `{"password":"a long app password","role":"owner"}`, owner); resp.StatusCode != 400 {
		t.Fatalf("owner role accepted for an app account: %d", resp.StatusCode)
	}
	if resp, body := e.req("PUT", "/api/v1/admin/app-accounts/admin", `{"password":"a long app password","role":"approver"}`, owner); resp.StatusCode != 200 {
		t.Fatalf("create: %d %v", resp.StatusCode, body)
	}
	resp, body := e.req("POST", "/api/v1/app/login", `{"username":"Admin","password":"a long app password","device":"Pixel 8"}`, nil)
	if resp.StatusCode != 200 || !strings.HasPrefix(body["token"].(string), "aura_m_") || body["role"] != "approver" {
		t.Fatalf("login: %d %v", resp.StatusCode, body)
	}
	dev := map[string]string{"Authorization": "Bearer " + body["token"].(string)}
	if resp, _ := e.req("GET", "/api/v1/tasks", "", dev); resp.StatusCode != 200 {
		t.Fatalf("device token cannot read tasks: %d", resp.StatusCode)
	}
	if resp, _ := e.req("GET", "/api/v1/admin/app-accounts", "", dev); resp.StatusCode != 403 {
		t.Fatalf("device token reached owner admin: %d", resp.StatusCode)
	}
	var stored int
	e.a.Store.R.QueryRow("select count(*) from members where token_hash like ?", "%"+body["token"].(string)+"%").Scan(&stored)
	if stored != 0 {
		t.Fatal("the device token is stored in clear")
	}
	// Logout revokes this device.
	e.req("POST", "/api/v1/app/logout", "", dev)
	if resp, _ := e.req("GET", "/api/v1/tasks", "", dev); resp.StatusCode != 401 {
		t.Fatalf("token valid after logout: %d", resp.StatusCode)
	}
	// Unknown user and wrong password look the same; 5 failures lock the account.
	if resp, b := e.req("POST", "/api/v1/app/login", `{"username":"nobody","password":"x"}`, nil); resp.StatusCode != 401 || !strings.Contains(fmtJSON(b), "wrong username or password") {
		t.Fatalf("unknown user: %d", resp.StatusCode)
	}
	for i := 0; i < 5; i++ {
		e.req("POST", "/api/v1/app/login", `{"username":"admin","password":"wrong wrong"}`, nil)
	}
	if resp, _ := e.req("POST", "/api/v1/app/login", `{"username":"admin","password":"a long app password"}`, nil); resp.StatusCode != 423 && resp.StatusCode != 429 {
		t.Fatalf("not locked/limited after failures: %d", resp.StatusCode)
	}
	var locked int64
	e.a.Store.R.QueryRow("select locked_until from app_accounts where username='admin'").Scan(&locked)
	if locked == 0 {
		t.Fatal("account not locked")
	}
	// Resetting the password unlocks and signs out every device.
	resp, body = e.req("PUT", "/api/v1/admin/app-accounts/admin", `{"password":"another long password","role":"operator"}`, owner)
	if resp.StatusCode != 200 {
		t.Fatalf("reset: %d %v", resp.StatusCode, body)
	}
	var active int
	e.a.Store.R.QueryRow("select count(*) from members where account_id is not null and disabled_at is null").Scan(&active)
	if active != 0 {
		t.Fatalf("devices still active after a password change: %d", active)
	}
}
