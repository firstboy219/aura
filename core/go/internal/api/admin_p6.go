package api

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
)

// Enterprise Admin (Blueprint D.1 P6, ADR-0049): departments, audit search and
// export, and usage by member and department. Owner only (routeRole).

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) setDepartment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Department string `json:"department"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if in.Department != "" && !deptRe.MatchString(in.Department) {
		s.fail(w, r, 400, "invalid_input", "department must be 1..40 characters (letters, digits, space . _ & -), or empty to clear")
		return
	}
	var n int64
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "update members set department=? where id=?", nullable(in.Department), r.PathValue("id"))
		if err != nil {
			return err
		}
		if n, _ = res.RowsAffected(); n == 0 {
			return nil
		}
		return policy.Audit(r.Context(), tx, "MEMBER_DEPARTMENT_SET", "owner", "", requestID(r), map[string]any{"member_id": r.PathValue("id"), "department": in.Department})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, 404, "not_found", "no member with that id")
		return
	}
	writeJSON(w, 200, map[string]any{"id": r.PathValue("id"), "department": in.Department})
}

// timeParam accepts RFC 3339 or a date (YYYY-MM-DD, in the workspace time zone).
func (s *Server) timeParam(v string, def time.Time) (time.Time, error) {
	if v == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", v, s.Engine.Location())
}

type auditRow struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Actor     string          `json:"actor"`
	TaskID    *string         `json:"task_id"`
	RequestID *string         `json:"request_id"`
	Data      json.RawMessage `json:"data"`
	At        string          `json:"at"`
}

// auditSearch filters the append-only audit log: type (comma list), actor,
// task_id, since/until, q (substring of the event data). format=csv or jsonl
// exports (up to 50000 rows) and the export itself is audited.
func (s *Server) auditSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now()
	since, err1 := s.timeParam(q.Get("since"), now.AddDate(0, 0, -30))
	until, err2 := s.timeParam(q.Get("until"), now.Add(time.Minute))
	format := q.Get("format")
	if err1 != nil || err2 != nil || (format != "" && format != "json" && format != "csv" && format != "jsonl") {
		s.fail(w, r, 400, "invalid_input", "since/until must be RFC 3339 or YYYY-MM-DD; format json|csv|jsonl")
		return
	}
	limit, max := 200, 1000
	if format == "csv" || format == "jsonl" {
		limit, max = 50000, 50000
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, max)
	}
	where, args := []string{"at >= ?", "at < ?"}, []any{since.UnixMilli(), until.UnixMilli()}
	if t := q.Get("type"); t != "" {
		types := strings.Split(t, ",")
		where = append(where, "type in (?"+strings.Repeat(",?", len(types)-1)+")")
		for _, x := range types {
			args = append(args, strings.TrimSpace(x))
		}
	}
	for _, f := range []string{"actor", "task_id"} {
		if v := q.Get(f); v != "" {
			where, args = append(where, f+" = ?"), append(args, v)
		}
	}
	if v := q.Get("q"); v != "" {
		esc := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(v)
		where, args = append(where, `data_json like ? escape '\'`), append(args, "%"+esc+"%")
	}
	rows, err := s.Store.R.QueryContext(r.Context(), "select id, type, actor, task_id, request_id, data_json, at from audit_events where "+
		strings.Join(where, " and ")+" order by at desc, id desc limit ?", append(args, limit)...)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	out := []auditRow{}
	for rows.Next() {
		var a auditRow
		var data string
		var at int64
		rows.Scan(&a.ID, &a.Type, &a.Actor, &a.TaskID, &a.RequestID, &data, &at)
		a.Data, a.At = json.RawMessage(data), ts(at)
		out = append(out, a)
	}
	rows.Close()
	if format == "" || format == "json" {
		writeJSON(w, 200, map[string]any{"events": out, "count": len(out), "truncated": len(out) == limit})
		return
	}
	s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		return policy.Audit(r.Context(), tx, "AUDIT_EXPORTED", PrincipalOf(r).ID, "", requestID(r), map[string]any{"format": format, "rows": len(out),
			"filter": r.URL.RawQuery})
	})
	name := fmt.Sprintf("aura-audit-%s.%s", time.Now().In(s.Engine.Location()).Format("20060102-150405"), format)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if format == "jsonl" {
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		for _, a := range out {
			enc.Encode(a)
		}
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	cw.Write([]string{"id", "at", "type", "actor", "task_id", "request_id", "data"})
	for _, a := range out {
		cw.Write([]string{a.ID, a.At, a.Type, a.Actor, deref(a.TaskID), deref(a.RequestID), csvSafe(string(a.Data))})
	}
	cw.Flush()
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// csvSafe stops spreadsheet formula injection from event data.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

type usageGroup struct {
	Key          string  `json:"key"`
	Tasks        int     `json:"tasks"`
	Calls        int     `json:"calls"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

// usageReport sums model usage by department, member, day (workspace time
// zone), tier, task_class or model. A subtask's usage counts for the member who
// created its root task; work AURA started itself (schedules, improvement, Lab)
// is reported as "system".
func (s *Server) usageReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now()
	since, err1 := s.timeParam(q.Get("since"), now.AddDate(0, 0, -30))
	until, err2 := s.timeParam(q.Get("until"), now.Add(time.Minute))
	by := q.Get("group_by")
	if by == "" {
		by = "department"
	}
	if err1 != nil || err2 != nil || !strings.Contains(",department,member,day,tier,task_class,model,", ","+by+",") {
		s.fail(w, r, 400, "invalid_input", "group_by department|member|day|tier|task_class|model; since/until RFC 3339 or YYYY-MM-DD")
		return
	}
	rows, err := s.Store.R.QueryContext(r.Context(), `select u.created_at, coalesce(u.tier,''), coalesce(u.api_model_id,''),
		coalesce(u.cost_actual_usd, u.cost_estimated_usd), coalesce(u.input_tokens,0), coalesce(u.output_tokens,0),
		coalesce(root.id,''), coalesce(root.task_class,''), coalesce(root.created_by,''), coalesce(m.name,''), coalesce(m.department,'')
		from usage_records u left join tasks t on t.id = u.task_id left join tasks root on root.id = t.root_task_id
		left join members m on m.id = root.created_by where u.created_at >= ? and u.created_at < ?`, since.UnixMilli(), until.UnixMilli())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	defer rows.Close()
	loc := s.Engine.Location()
	groups, tasks := map[string]*usageGroup{}, map[string]map[string]bool{}
	var total usageGroup
	for rows.Next() {
		var at, in, outT int64
		var tier, model, root, class, creator, member, dept string
		var cost float64
		rows.Scan(&at, &tier, &model, &cost, &in, &outT, &root, &class, &creator, &member, &dept)
		key := ""
		switch by {
		case "department":
			switch {
			case creator == "owner":
				key = "owner"
			case member != "" && dept != "":
				key = dept
			case member != "":
				key = "(no department)"
			default:
				key = "system"
			}
		case "member":
			switch {
			case creator == "owner":
				key = "owner"
			case member != "":
				key = member
			default:
				key = "system"
			}
		case "day":
			key = time.UnixMilli(at).In(loc).Format("2006-01-02")
		case "tier":
			key = tier
		case "task_class":
			key = class
		case "model":
			key = model
		}
		g := groups[key]
		if g == nil {
			g, tasks[key] = &usageGroup{Key: key}, map[string]bool{}
			groups[key] = g
		}
		for _, x := range []*usageGroup{g, &total} {
			x.Calls++
			x.CostUSD += cost
			x.InputTokens += in
			x.OutputTokens += outT
		}
		if root != "" {
			tasks[key][root] = true
		}
	}
	out := []*usageGroup{}
	for k, g := range groups {
		g.Tasks = len(tasks[k])
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CostUSD > out[j].CostUSD || (out[i].CostUSD == out[j].CostUSD && out[i].Key < out[j].Key)
	})
	total.Key = "total"
	writeJSON(w, 200, map[string]any{"group_by": by, "since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339),
		"time_zone": loc.String(), "groups": out, "total": total, "generated_at": ts(ids.NowMs())})
}
