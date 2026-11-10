package app

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/task"
)

// D.1 P6 Enterprise Admin: usage by department and member (a subtask counts for
// its root task's creator), audit search and export (export audited, CSV safe
// from formula injection), owner only.
func TestEnterpriseAdminAuditAndUsage(t *testing.T) {
	e := newEnv(t, 1000)
	owner := e.auth()
	tok := map[string]string{}
	for _, m := range []struct{ name, dept string }{{"ana", "Sales"}, {"budi", "Ops"}, {"citra", ""}} {
		resp, body := e.req("POST", "/api/v1/admin/members", `{"name":"`+m.name+`","role":"approver","department":"`+m.dept+`"}`, owner)
		if resp.StatusCode != 201 {
			t.Fatalf("create member: %d %v", resp.StatusCode, body)
		}
		tok[m.name] = body["token"].(string)
	}
	if resp, _ := e.req("POST", "/api/v1/admin/members", `{"name":"x","role":"viewer","department":"=cmd|calc"}`, owner); resp.StatusCode != 400 {
		t.Fatalf("bad department accepted: %d", resp.StatusCode)
	}
	as := func(n string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok[n]} }
	objective := `{"objective":"Fetch https://example.com/ and write reports/a.md","tools":["web.fetch","workspace.write"],"allow_domains":["example.com"]}`
	created := map[string]string{}
	for _, who := range []string{"ana", "budi", "citra", "owner"} {
		h := owner
		if who != "owner" {
			h = as(who)
		}
		resp, body := e.req("POST", "/api/v1/tasks", objective, h)
		if resp.StatusCode != 201 {
			t.Fatalf("task by %s: %d %v", who, resp.StatusCode, body)
		}
		created[who] = body["id"].(string)
	}
	ctx := context.Background()
	e.a.Engine.Stop(ctx, "owner") // the usage below is a fixture; nothing else may add any
	usage := func(tx *sql.Tx, taskID string, cost float64) error {
		_, err := tx.Exec(`insert into usage_records(id, task_id, tier, api_model_id, input_tokens, output_tokens, cost_estimated_usd,
			cost_reserved_usd, cost_actual_usd, outcome, created_at) values(?,?,?,?,?,?,?,?,?,?,?)`,
			ids.New(), taskID, "standard", "m/x", 100, 10, cost, cost, cost, "ok", ids.NowMs())
		return err
	}
	e.a.Store.Write(ctx, func(tx *sql.Tx) error {
		tx.Exec("delete from usage_records")
		usage(tx, created["ana"], 0.30)
		usage(tx, created["budi"], 0.20)
		usage(tx, created["citra"], 0.05)
		usage(tx, created["owner"], 0.10)
		// A subtask of ana's task: counts for Sales.
		rootID := created["ana"]
		child := &task.Task{Objective: "child", ConfigHash: "h", ParentTaskID: &rootID, RootTaskID: rootID, AgentRole: "specialist", Depth: 1}
		if err := task.Insert(ctx, tx, child); err != nil {
			return err
		}
		usage(tx, child.ID, 0.15)
		// Work AURA started itself.
		sys := &task.Task{Objective: "[improvement] x", ConfigHash: "h", AgentRole: "improvement"}
		task.Insert(ctx, tx, sys)
		return usage(tx, sys.ID, 0.01)
	})
	resp, rep := e.req("GET", "/api/v1/admin/usage?group_by=department", "", owner)
	if resp.StatusCode != 200 {
		t.Fatalf("usage: %d %v", resp.StatusCode, rep)
	}
	got := map[string]float64{}
	tasks := map[string]float64{}
	for _, g := range rep["groups"].([]any) {
		m := g.(map[string]any)
		got[m["key"].(string)] = m["cost_usd"].(float64)
		tasks[m["key"].(string)] = m["tasks"].(float64)
	}
	near := func(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }
	if !near(got["Sales"], 0.45) || !near(got["Ops"], 0.20) || !near(got["(no department)"], 0.05) || !near(got["owner"], 0.10) ||
		!near(got["system"], 0.01) || tasks["Sales"] != 1 {
		t.Fatalf("usage by department: %v (tasks %v)", got, tasks)
	}
	if _, rep := e.req("GET", "/api/v1/admin/usage?group_by=member", "", owner); !strings.Contains(fmtJSON(rep), `"key":"ana"`) {
		t.Fatalf("usage by member: %v", rep)
	}
	if resp, _ := e.req("GET", "/api/v1/admin/usage?group_by=password", "", owner); resp.StatusCode != 400 {
		t.Fatalf("bad group_by: %d", resp.StatusCode)
	}
	// Audit search.
	resp, res := e.req("GET", "/api/v1/admin/audit?type=MEMBER_CREATED&q=Sales", "", owner)
	if resp.StatusCode != 200 || res["count"].(float64) != 1 {
		t.Fatalf("audit search: %d %v", resp.StatusCode, res)
	}
	if _, res := e.req("GET", "/api/v1/admin/audit?q=%25", "", owner); res["count"].(float64) != 0 {
		t.Fatalf("a LIKE wildcard in q matched everything: %v", res["count"])
	}
	// Export: CSV, audited, formula-safe.
	e.a.Store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("insert into audit_events(id, type, actor, data_json, at) values(?,?,?,?,?)", ids.New(), "TEST_EVENT", "owner", `=HYPERLINK("x")`, ids.NowMs())
		return err
	})
	r, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/admin/audit?format=csv&type=TEST_EVENT", nil)
	r.Header.Set("Authorization", "Bearer "+e.token)
	hr, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(hr.Body)
	hr.Body.Close()
	recs, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil || hr.StatusCode != 200 || !strings.Contains(hr.Header.Get("Content-Disposition"), "attachment") || len(recs) != 2 ||
		!strings.HasPrefix(recs[1][6], "'=") {
		t.Fatalf("csv export: %d %v %q", hr.StatusCode, err, raw)
	}
	var exported int
	e.a.Store.R.QueryRow("select count(*) from audit_events where type='AUDIT_EXPORTED'").Scan(&exported)
	if exported != 1 {
		t.Fatalf("export not audited: %d", exported)
	}
	// Owner only.
	for _, p := range []string{"/api/v1/admin/audit", "/api/v1/admin/usage"} {
		if resp, _ := e.req("GET", p, "", as("ana")); resp.StatusCode != 403 {
			t.Fatalf("%s as approver: %d", p, resp.StatusCode)
		}
	}
	_, list := e.req("GET", "/api/v1/admin/members", "", owner)
	var budiID string
	for _, m := range list["members"].([]any) {
		if mm := m.(map[string]any); mm["name"] == "budi" {
			budiID = mm["id"].(string)
		}
	}
	if resp, body := e.req("POST", "/api/v1/admin/members/"+budiID+"/department", `{"department":"Finance"}`, owner); resp.StatusCode != 200 {
		t.Fatalf("set department: %d %v", resp.StatusCode, body)
	}
	if _, rep := e.req("GET", "/api/v1/admin/usage?group_by=department", "", owner); !strings.Contains(fmtJSON(rep), `"key":"Finance"`) {
		t.Fatalf("department change not reflected: %v", rep)
	}
}

func fmtJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
