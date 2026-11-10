package api

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/webauth"
)

// App accounts (ADR-0053): the Android client signs in with a username and
// password and receives a device token (a member token linked to the account).
// Accounts are created by the owner; their role is at most approver, so the
// owner-only actions (credentials, tools, workflows, admin) stay with the owner.

// dummyHash keeps a sign-in for an unknown user as slow as one for a known user.
var dummyHash, _ = webauth.HashPassword("not-a-real-password-" + webauth.RandomToken())

var (
	appUserRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)
	appDeviceRe = regexp.MustCompile(`[^A-Za-z0-9 ._-]`)
)

// appLogin is unauthenticated: rate limited per address and locked per account
// after repeated failures. Wrong user and wrong password look the same.
func (s *Server) appLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.loginLim.allow(ip) {
		s.fail(w, r, 429, "rate_limited", "too many attempts; wait a minute")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Device   string `json:"device"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	user := strings.ToLower(strings.TrimSpace(in.Username))
	var id, hash, role string
	var failed int
	var locked int64
	var disabled sql.NullInt64
	err := s.Store.R.QueryRowContext(r.Context(), "select id, password_hash, role, failed_logins, locked_until, disabled_at from app_accounts where username=?", user).
		Scan(&id, &hash, &role, &failed, &locked, &disabled)
	now := time.Now()
	if errors.Is(err, sql.ErrNoRows) || disabled.Valid {
		webauth.VerifyPassword(dummyHash, in.Password) // same work either way
		s.audit(r, "APP_LOGIN_FAILED", map[string]any{"username": truncateStr(user, 40), "remote": ip})
		s.fail(w, r, 401, "unauthorized", "wrong username or password")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if locked > now.UnixMilli() {
		s.audit(r, "APP_LOGIN_LOCKED", map[string]any{"username": user, "remote": ip})
		s.fail(w, r, 423, "locked", "too many failed sign-ins; try again in 15 minutes")
		return
	}
	if webauth.VerifyPassword(hash, in.Password) != nil {
		s.Store.Write(r.Context(), func(tx *sql.Tx) error {
			f, lock := failed+1, int64(0)
			if f >= maxFailures {
				lock, f = now.Add(lockFor).UnixMilli(), 0
			}
			if _, err := tx.ExecContext(r.Context(), "update app_accounts set failed_logins=?, locked_until=?, updated_at=? where id=?", f, lock, now.UnixMilli(), id); err != nil {
				return err
			}
			return policy.Audit(r.Context(), tx, "APP_LOGIN_FAILED", "app", "", requestID(r), map[string]any{"username": user, "remote": ip, "locked": lock > 0})
		})
		s.fail(w, r, 401, "unauthorized", "wrong username or password")
		return
	}
	device := strings.TrimSpace(appDeviceRe.ReplaceAllString(in.Device, ""))
	if device == "" {
		device = "android"
	}
	tok := "aura_m_" + webauth.RandomToken()
	memberID := ids.New()
	name := "app:" + user + ":" + truncateStr(device, 20) + ":" + strings.ToLower(memberID[len(memberID)-6:])
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "update app_accounts set failed_logins=0, locked_until=0, updated_at=? where id=?", now.UnixMilli(), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "insert into members(id, name, role, token_hash, account_id, created_at) values(?,?,?,?,?,?)",
			memberID, name, role, tokenHash(tok), id, now.UnixMilli()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "APP_LOGIN", "app", "", requestID(r), map[string]any{"username": user, "device": device, "member_id": memberID, "remote": ip})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"token": tok, "role": role, "member_id": memberID, "username": user})
}

// appLogout revokes the calling device token.
func (s *Server) appLogout(w http.ResponseWriter, r *http.Request) {
	p := PrincipalOf(r)
	if p.Role == RoleOwner {
		s.fail(w, r, 400, "invalid_input", "the owner token is not a device token")
		return
	}
	s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "update members set disabled_at=? where id=? and disabled_at is null", time.Now().UnixMilli(), p.ID); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "APP_LOGOUT", p.Name, "", requestID(r), map[string]any{"member_id": p.ID})
	})
	writeJSON(w, 200, map[string]any{"signed_out": true})
}

func (s *Server) listAppAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.R.QueryContext(r.Context(), `select a.id, a.username, a.role, a.created_at, a.disabled_at,
		(select count(*) from members m where m.account_id = a.id and m.disabled_at is null) from app_accounts a order by a.username`)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, user, role string
		var created int64
		var disabled sql.NullInt64
		var devices int
		rows.Scan(&id, &user, &role, &created, &disabled, &devices)
		out = append(out, map[string]any{"id": id, "username": user, "role": role, "created_at": ts(created), "disabled": disabled.Valid, "active_devices": devices})
	}
	writeJSON(w, 200, map[string]any{"accounts": out})
}

// putAppAccount creates an account or sets its password and role (owner only).
func (s *Server) putAppAccount(w http.ResponseWriter, r *http.Request) {
	user := strings.ToLower(r.PathValue("username"))
	var in struct {
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if !appUserRe.MatchString(user) || (in.Role != RoleViewer && in.Role != RoleOperator && in.Role != RoleApprover) || len(in.Password) < 10 || len(in.Password) > 200 {
		s.fail(w, r, 400, "invalid_input", "username [a-z0-9._-]{1,40}, role viewer|operator|approver, password 10..200 characters")
		return
	}
	hash, err := webauth.HashPassword(in.Password)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	now := ids.NowMs()
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), `insert into app_accounts(id, username, password_hash, role, created_at, updated_at) values(?,?,?,?,?,?)
			on conflict(username) do update set password_hash=excluded.password_hash, role=excluded.role, failed_logins=0, locked_until=0,
			disabled_at=null, updated_at=excluded.updated_at`, ids.New(), user, hash, in.Role, now, now); err != nil {
			return err
		}
		// A new password signs out every device of the account.
		if _, err := tx.ExecContext(r.Context(), "update members set disabled_at=? where account_id=(select id from app_accounts where username=?) and disabled_at is null", now, user); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "APP_ACCOUNT_SET", "owner", "", requestID(r), map[string]any{"username": user, "role": in.Role})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"username": user, "role": in.Role})
}

// disableAppAccount disables an account and revokes all its device tokens.
func (s *Server) disableAppAccount(w http.ResponseWriter, r *http.Request) {
	user := strings.ToLower(r.PathValue("username"))
	var n int64
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		now := ids.NowMs()
		res, err := tx.ExecContext(r.Context(), "update app_accounts set disabled_at=?, updated_at=? where username=? and disabled_at is null", now, now, user)
		if err != nil {
			return err
		}
		if n, _ = res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := tx.ExecContext(r.Context(), "update members set disabled_at=? where account_id=(select id from app_accounts where username=?) and disabled_at is null", now, user); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "APP_ACCOUNT_DISABLED", "owner", "", requestID(r), map[string]any{"username": user})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, 404, "not_found", "no active account with that username")
		return
	}
	writeJSON(w, 200, map[string]any{"disabled": user})
}
