package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func spatialBody(extra string) string {
	return `{"purpose":"static_image_analysis","modalities":["image"],"capture_scope":["table_top"],"duration_s":300,"consent_ref":"consent-1","foreground":true` + extra + `}`
}

// ADR-0081: the route exists only after the owner turns it on; it authorizes scope only; ending is final; every denial and change is audited.
func TestSpatialSessionsAreOffByDefaultAndAuthorizeScopeOnly(t *testing.T) {
	e := newWebEnv(t)
	owner := e.auth()
	mk := func(role string) map[string]string {
		resp, b := e.req("POST", "/api/v1/admin/members", `{"name":"sp-`+role+`","role":"`+role+`"}`, owner)
		if resp.StatusCode != 201 {
			t.Fatalf("member %s: %d", role, resp.StatusCode)
		}
		return map[string]string{"Authorization": "Bearer " + b["token"].(string)}
	}
	approver, operator := mk("approver"), mk("operator")

	if resp, b := e.req("POST", "/api/v1/spatial/sessions", spatialBody(""), approver); resp.StatusCode != 403 || b["error"].(map[string]any)["code"] != "spatial_disabled" {
		t.Fatalf("off by default: %d %v", resp.StatusCode, b)
	}
	if resp, b := e.req("GET", "/api/v1/admin/spatial", "", owner); resp.StatusCode != 200 || b["enabled"] != false {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
	if resp, _ := e.req("PUT", "/api/v1/admin/spatial", `{"enabled":true}`, approver); resp.StatusCode != 403 {
		t.Fatalf("only the owner flips the flag: %d", resp.StatusCode)
	}
	if resp, b := e.req("PUT", "/api/v1/admin/spatial", `{"enabled":true}`, owner); resp.StatusCode != 200 || b["enabled"] != true {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}

	if resp, _ := e.req("POST", "/api/v1/spatial/sessions", spatialBody(""), operator); resp.StatusCode != 403 {
		t.Fatalf("an operator may not authorize capture: %d", resp.StatusCode)
	}
	if resp, b := e.req("POST", "/api/v1/spatial/sessions", strings.Replace(spatialBody(""), `"foreground":true`, `"foreground":false`, 1), approver); resp.StatusCode != 403 {
		t.Fatalf("background refused: %d %v", resp.StatusCode, b)
	}
	if resp, _ := e.req("POST", "/api/v1/spatial/sessions", spatialBody(`,"surprise":1`), approver); resp.StatusCode != 400 {
		t.Fatalf("unknown fields are refused: %d", resp.StatusCode)
	}

	resp, s := e.req("POST", "/api/v1/spatial/sessions", spatialBody(""), approver)
	if resp.StatusCode != 201 || s["state"] != "authorized" || s["retention_class"] != "session_ephemeral" || s["ampi_state"] != "not_connected" || s["ampi_session_ref"] != nil || s["policy_result"] != "authorized" {
		t.Fatalf("%d %v", resp.StatusCode, s)
	}
	if !strings.Contains(s["note"].(string), "nothing is being captured") {
		t.Fatalf("the response must say that nothing is captured: %v", s["note"])
	}
	id := s["session_id"].(string)
	if resp, _ := e.req("POST", "/api/v1/spatial/sessions", spatialBody(""), approver); resp.StatusCode != 409 {
		t.Fatalf("one session at a time: %d", resp.StatusCode)
	}
	if resp, b := e.req("GET", "/api/v1/spatial/sessions/"+id, "", operator); resp.StatusCode != 200 || b["state"] != "authorized" {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}

	// stop is final; stopping or revoking again conflicts; a new session can then be authorized
	if resp, b := e.req("POST", "/api/v1/spatial/sessions/"+id+"/stop", "", approver); resp.StatusCode != 200 || b["state"] != "stopped" {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
	if resp, _ := e.req("POST", "/api/v1/spatial/sessions/"+id+"/revoke", "", approver); resp.StatusCode != 409 {
		t.Fatalf("an ended session is never reopened or re-ended: %d", resp.StatusCode)
	}
	if resp, _ := e.req("POST", "/api/v1/spatial/sessions/"+id+"/resume", "", approver); resp.StatusCode != 404 {
		t.Fatalf("there is no resume: %d", resp.StatusCode)
	}
	_, s2 := e.req("POST", "/api/v1/spatial/sessions", spatialBody(""), approver)
	id2 := s2["session_id"].(string)

	// turning the feature off revokes what is authorized, and the audit chain names every step without content
	if resp, b := e.req("PUT", "/api/v1/admin/spatial", `{"enabled":false}`, owner); resp.StatusCode != 200 || b["enabled"] != false || b["active_sessions"] != float64(0) {
		t.Fatalf("%d %v", resp.StatusCode, b)
	}
	if resp, _ := e.req("GET", "/api/v1/spatial/sessions/"+id2, "", approver); resp.StatusCode != 403 {
		t.Fatalf("disabled again: %d", resp.StatusCode)
	}
	for _, typ := range []string{"SPATIAL_FLAG_CHANGED", "SPATIAL_SESSION_DENIED", "SPATIAL_SESSION_AUTHORIZED", "SPATIAL_SESSION_STOPPED"} {
		var n int
		e.a.Store.R.QueryRow("select count(*) from audit_events where type=?", typ).Scan(&n)
		if n == 0 {
			t.Errorf("audit lacks %s", typ)
		}
	}
	var named int
	if err := e.a.Store.R.QueryRow("select count(*) from audit_events where type='SPATIAL_SESSION_AUTHORIZED' and data_json like '%static_image_analysis%'").Scan(&named); err != nil || named == 0 {
		t.Errorf("the audit row must name the purpose: %v %d", err, named)
	}
	var leaked int
	if err := e.a.Store.R.QueryRow("select count(*) from audit_events where type like 'SPATIAL_%' and (data_json like '%consent-1%' or data_json like '%table_top%')").Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("the audit must name ids and purposes, not consent or scope content")
	}
}

// Expiry ends a session by itself; nothing resumes.
func TestSpatialSessionExpiresOnItsOwn(t *testing.T) {
	e := newWebEnv(t)
	owner := e.auth()
	e.req("PUT", "/api/v1/admin/spatial", `{"enabled":true}`, owner)
	_, s := e.req("POST", "/api/v1/spatial/sessions", spatialBody(""), owner)
	id := s["session_id"].(string)
	if err := e.a.Store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("update spatial_sessions set expires_at=1 where id=?", id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, b := e.req("GET", "/api/v1/spatial/sessions/"+id, "", owner); b["state"] != "expired" || b["state_reason"] != "time_limit" {
		t.Fatalf("%v", b)
	}
}
