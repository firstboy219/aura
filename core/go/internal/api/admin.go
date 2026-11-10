package api

import (
	"database/sql"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/webauth"
)

// Workspace Admin foundation (Blueprint D.1 P5, ADR-0043): members and roles,
// the daily budget, and write-only connector credentials. Owner only.

var (
	memberNameRe = regexp.MustCompile(`^[A-Za-z0-9 ._@-]{1,60}$`)
	credNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,40}$`)
)

// SettingDailyBudget is the settings key of the owner's daily budget override.
const SettingDailyBudget = "daily_budget_usd"

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.R.QueryContext(r.Context(), "select id, name, role, created_at, disabled_at, last_used_at from members order by created_at")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, role string
		var created int64
		var disabled, used sql.NullInt64
		rows.Scan(&id, &name, &role, &created, &disabled, &used)
		m := map[string]any{"id": id, "name": name, "role": role, "created_at": ts(created), "disabled": disabled.Valid}
		if used.Valid {
			m["last_used_at"] = ts(used.Int64)
		}
		out = append(out, m)
	}
	writeJSON(w, 200, map[string]any{"members": out})
}

// createMember returns the member's token once; only its hash is stored.
func (s *Server) createMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if !memberNameRe.MatchString(in.Name) || (in.Role != RoleViewer && in.Role != RoleOperator && in.Role != RoleApprover) {
		s.fail(w, r, 400, "invalid_input", "name (1..60 chars) and role viewer|operator|approver are required")
		return
	}
	tok := "aura_m_" + webauth.RandomToken()
	id := ids.New()
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "insert into members(id, name, role, token_hash, created_at) values(?,?,?,?,?)",
			id, in.Name, in.Role, tokenHash(tok), ids.NowMs()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "MEMBER_CREATED", "owner", "", requestID(r), map[string]any{"member": in.Name, "role": in.Role})
	})
	if err != nil {
		s.fail(w, r, 409, "conflict", "a member with that name exists")
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "name": in.Name, "role": in.Role, "token": tok,
		"note": "shown once; give it to the member and keep it secret"})
}

func (s *Server) disableMember(w http.ResponseWriter, r *http.Request) {
	var n int64
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "update members set disabled_at=? where id=? and disabled_at is null", ids.NowMs(), r.PathValue("id"))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return policy.Audit(r.Context(), tx, "MEMBER_DISABLED", "owner", "", requestID(r), map[string]any{"member_id": r.PathValue("id")})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, 404, "not_found", "no active member with that id")
		return
	}
	writeJSON(w, 200, map[string]any{"disabled": r.PathValue("id")})
}

func (s *Server) getBudget(w http.ResponseWriter, r *http.Request) {
	reserved, spent, _ := s.Governor.Today(r.Context())
	writeJSON(w, 200, map[string]any{"daily_budget_usd": s.Governor.Limit(), "spent_today_usd": spent, "reserved_today_usd": reserved})
}

func (s *Server) putBudget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DailyBudgetUSD float64 `json:"daily_budget_usd"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if in.DailyBudgetUSD < 0 || in.DailyBudgetUSD > 1000 {
		s.fail(w, r, 400, "invalid_input", "daily_budget_usd must be 0..1000")
		return
	}
	v := strconv.FormatFloat(in.DailyBudgetUSD, 'f', -1, 64)
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `insert into settings(key, value, updated_at) values(?,?,?)
			on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, SettingDailyBudget, v, ids.NowMs()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "BUDGET_CHANGED", "owner", "", requestID(r), map[string]any{"daily_budget_usd": in.DailyBudgetUSD,
			"previous": s.Governor.Limit()})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.Governor.SetLimit(in.DailyBudgetUSD)
	writeJSON(w, 200, map[string]any{"daily_budget_usd": in.DailyBudgetUSD})
}

// Connector credentials are write-only: there is no endpoint that returns a value.
func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.R.QueryContext(r.Context(), "select substr(name, 11), updated_at from secrets where name like 'connector.%' order by name")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var name string
		var upd int64
		rows.Scan(&name, &upd)
		out = append(out, map[string]any{"name": name, "updated_at": time.UnixMilli(upd).UTC().Format(time.RFC3339), "ref": "vault:connector." + name})
	}
	writeJSON(w, 200, map[string]any{"credentials": out})
}

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var in struct {
		Value string `json:"value"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if !credNameRe.MatchString(name) || s.Vault == nil {
		s.fail(w, r, 400, "invalid_input", "name must match [a-z0-9][a-z0-9_.-]{0,40} and the vault must be configured")
		return
	}
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if err := s.Vault.Put(r.Context(), tx, "connector."+name, in.Value); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "CREDENTIAL_SET", "owner", "", requestID(r), map[string]any{"name": name})
	})
	if err != nil {
		s.fail(w, r, 400, "invalid_input", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"name": name, "ref": "vault:connector." + name, "stored": true})
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var n int64
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "delete from secrets where name=?", "connector."+name)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return policy.Audit(r.Context(), tx, "CREDENTIAL_DELETED", "owner", "", requestID(r), map[string]any{"name": name})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, 404, "not_found", "no credential with that name")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": name})
}
