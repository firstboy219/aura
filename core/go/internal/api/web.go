package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/vault"
	"aura.local/core/internal/webauth"
	"aura.local/core/internal/webui"
)

// Workspace Web App owner authentication (Blueprint 13A.4, D.1 P5 web
// security tests, ADR-0041).

// WebConfig is the web part of the server configuration.
type WebConfig struct {
	Enabled      bool
	Origin       string // https://aura.example.com
	APIHosts     []string
	SessionHours int
	TrustedProxy []netip.Prefix
}

const (
	sessionCookie = "__Host-aura_session" // __Host-: Secure, Path=/, no Domain
	totpSecretKey = "web-totp"
	maxFailures   = 5
	lockFor       = 15 * time.Minute
)

func (s *Server) webHost() string {
	if u, err := url.Parse(s.Web.Origin); err == nil {
		return strings.ToLower(u.Host)
	}
	return ""
}

func (s *Server) isWebHost(r *http.Request) bool {
	return s.Web.Enabled && strings.EqualFold(r.Host, s.webHost())
}

// clientIP is the connecting address, or CF-Connecting-IP when the request
// comes from the trusted tunnel connector.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil {
		for _, p := range s.Web.TrustedProxy {
			if p.Contains(a.Unmap()) {
				if cf := r.Header.Get("CF-Connecting-IP"); cf != "" {
					return cf
				}
			}
		}
	}
	return host
}

// webSession authorizes a request by session cookie; "" means authorized.
func (s *Server) webSession(w http.ResponseWriter, r *http.Request) string {
	if !s.isWebHost(r) {
		return "no bearer token"
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "no session"
	}
	var csrf string
	var expires int64
	err = s.Store.R.QueryRowContext(r.Context(), "select csrf, expires_at from web_sessions where id_hash=?", webauth.Hash(c.Value)).Scan(&csrf, &expires)
	if err != nil || expires < time.Now().UnixMilli() {
		return "session expired"
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if o := r.Header.Get("Origin"); o != "" && !strings.EqualFold(o, s.Web.Origin) {
			return "cross-origin request"
		}
		if !webauth.ConstantEq(r.Header.Get("X-CSRF-Token"), csrf) {
			return "missing or wrong CSRF token"
		}
	}
	s.Store.Write(context.WithoutCancel(r.Context()), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), "update web_sessions set last_seen=? where id_hash=?", time.Now().UnixMilli(), webauth.Hash(c.Value))
		return err
	})
	return ""
}

// loginLimiter slows repeated attempts from one address (the account lockout
// below is the main control; this protects argon2 CPU).
type loginLimiter struct {
	mu   sync.Mutex
	last map[string][]time.Time
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string][]time.Time{}
	}
	cut := time.Now().Add(-time.Minute)
	var keep []time.Time
	for _, t := range l.last[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 10 {
		l.last[ip] = keep
		return false
	}
	l.last[ip] = append(keep, time.Now())
	return true
}

func (s *Server) webOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isWebHost(r) {
			s.fail(w, r, 404, "not_found", "no such endpoint")
			return
		}
		if o := r.Header.Get("Origin"); r.Method == http.MethodPost && o != "" && !strings.EqualFold(o, s.Web.Origin) {
			s.fail(w, r, 403, "forbidden", "cross-origin request")
			return
		}
		next(w, r)
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// webLogin: password + TOTP; generic errors; lockout after repeated failures.
func (s *Server) webLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.loginLim.allow(ip) {
		s.fail(w, r, 429, "rate_limited", "too many attempts; wait a minute")
		return
	}
	var in struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	var hash string
	var lastStep, locked int64
	var failed int
	err := s.Store.R.QueryRowContext(r.Context(), "select password_hash, totp_last_step, failed_logins, locked_until from web_owner where id=1").
		Scan(&hash, &lastStep, &failed, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		s.fail(w, r, 409, "not_set_up", "the owner account is not set up; create a setup link with aura web setup-link")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	now := time.Now()
	if locked > now.UnixMilli() {
		s.audit(r, "WEB_LOGIN_LOCKED", map[string]any{"remote": ip})
		s.fail(w, r, 423, "locked", "too many failed logins; try again later")
		return
	}
	secret := ""
	if s.Vault != nil {
		secret, _ = s.Vault.Get(r.Context(), s.Store.R, totpSecretKey)
	}
	pwErr := webauth.VerifyPassword(hash, in.Password)
	step, totpOK := webauth.VerifyTOTP(secret, in.Code, now, lastStep)
	if pwErr != nil || !totpOK || secret == "" {
		s.Store.Write(r.Context(), func(tx *sql.Tx) error {
			f := failed + 1
			lock := int64(0)
			if f >= maxFailures {
				lock, f = now.Add(lockFor).UnixMilli(), 0
			}
			_, err := tx.ExecContext(r.Context(), "update web_owner set failed_logins=?, locked_until=?, updated_at=? where id=1", f, lock, now.UnixMilli())
			if err == nil {
				err = policy.Audit(r.Context(), tx, "WEB_LOGIN_FAILED", "web", "", requestID(r), map[string]any{"remote": ip, "locked": lock > 0})
			}
			return err
		})
		s.fail(w, r, 401, "unauthorized", "wrong password or code")
		return
	}
	token, csrf := webauth.RandomToken(), webauth.RandomToken()
	exp := now.Add(time.Duration(s.Web.SessionHours) * time.Hour)
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "update web_owner set totp_last_step=?, failed_logins=0, locked_until=0, updated_at=? where id=1", step, now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "delete from web_sessions where expires_at < ?", now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "insert into web_sessions(id_hash, csrf, created_at, expires_at, last_seen, remote, user_agent) values(?,?,?,?,?,?,?)",
			webauth.Hash(token), csrf, now.UnixMilli(), exp.UnixMilli(), now.UnixMilli(), ip, truncateStr(r.UserAgent(), 200)); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "WEB_LOGIN", "owner", "", requestID(r), map[string]any{"remote": ip})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.setSessionCookie(w, token, int(time.Until(exp).Seconds()))
	writeJSON(w, 200, map[string]any{"csrf": csrf, "expires_at": exp.UTC().Format(time.RFC3339)})
}

func truncateStr(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

func (s *Server) webLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.Write(r.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), "delete from web_sessions where id_hash=?", webauth.Hash(c.Value))
			return err
		})
	}
	s.setSessionCookie(w, "", -1)
	writeJSON(w, 200, map[string]any{"logged_out": true})
}

// webSessionInfo tells the app whether it is signed in (and its CSRF token).
func (s *Server) webSessionInfo(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(sessionCookie)
	out := map[string]any{"authenticated": false}
	if err == nil {
		var csrf string
		var exp int64
		if s.Store.R.QueryRowContext(r.Context(), "select csrf, expires_at from web_sessions where id_hash=?", webauth.Hash(c.Value)).Scan(&csrf, &exp) == nil &&
			exp > time.Now().UnixMilli() {
			out = map[string]any{"authenticated": true, "csrf": csrf, "expires_at": time.UnixMilli(exp).UTC().Format(time.RFC3339)}
		}
	}
	var n int
	s.Store.R.QueryRowContext(r.Context(), "select count(*) from web_owner").Scan(&n)
	out["set_up"] = n > 0
	writeJSON(w, 200, out)
}

// webSetupStart: a valid one-time link + a new password → a TOTP secret to scan.
func (s *Server) webSetupStart(w http.ResponseWriter, r *http.Request) {
	if !s.loginLim.allow(s.clientIP(r)) {
		s.fail(w, r, 429, "rate_limited", "too many attempts")
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if s.Vault == nil {
		s.fail(w, r, 409, "no_vault", "the Web App needs secrets.master_key_file")
		return
	}
	hash, err := webauth.HashPassword(in.Password)
	if err != nil {
		s.fail(w, r, 400, "invalid_input", err.Error())
		return
	}
	secret := webauth.NewTOTPSecret()
	enc, err := s.Vault.Seal(totpSecretKey, secret)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	ok := false
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "update web_setup set password_hash=?, totp_pending=? where token_hash=? and used_at is null and expires_at > ?",
			hash, enc, webauth.Hash(in.Token), time.Now().UnixMilli())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n == 1
		return nil
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if !ok {
		s.fail(w, r, 401, "unauthorized", "the setup link is invalid, used or expired")
		return
	}
	account := s.webHost()
	writeJSON(w, 200, map[string]any{"otpauth_uri": webauth.TOTPURI(secret, "owner@"+account, "AURA"), "secret": secret})
}

// webSetupConfirm: the first code from the authenticator app activates the owner account.
func (s *Server) webSetupConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.loginLim.allow(s.clientIP(r)) {
		s.fail(w, r, 429, "rate_limited", "too many attempts")
		return
	}
	var in struct {
		Token string `json:"token"`
		Code  string `json:"code"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	var hash, enc sql.NullString
	err := s.Store.R.QueryRowContext(r.Context(), "select password_hash, totp_pending from web_setup where token_hash=? and used_at is null and expires_at > ?",
		webauth.Hash(in.Token), time.Now().UnixMilli()).Scan(&hash, &enc)
	if err != nil || !hash.Valid || !enc.Valid || s.Vault == nil {
		s.fail(w, r, 401, "unauthorized", "the setup link is invalid, used or expired")
		return
	}
	secret, err := s.Vault.Open(totpSecretKey, enc.String)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	step, ok := webauth.VerifyTOTP(secret, in.Code, time.Now(), 0)
	if !ok {
		s.fail(w, r, 401, "unauthorized", "wrong code; check the time on your phone and try the next code")
		return
	}
	now := time.Now().UnixMilli()
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if err := s.Vault.Put(r.Context(), tx, totpSecretKey, secret); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `insert into web_owner(id, password_hash, totp_last_step, created_at, updated_at) values(1,?,?,?,?)
			on conflict(id) do update set password_hash=excluded.password_hash, totp_last_step=excluded.totp_last_step, failed_logins=0, locked_until=0,
			updated_at=excluded.updated_at`, hash.String, step, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "update web_setup set used_at=?, password_hash=null, totp_pending=null where token_hash=?", now, webauth.Hash(in.Token)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), "delete from web_sessions"); err != nil { // a reset signs every device out
			return err
		}
		return policy.Audit(r.Context(), tx, "WEB_OWNER_SET_UP", "owner", "", requestID(r), map[string]any{"remote": s.clientIP(r)})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"set_up": true})
}

// CreateSetupLink makes a one-time owner setup link (CLI, inside the Core container).
func CreateSetupLink(ctx context.Context, st interface {
	Write(context.Context, func(*sql.Tx) error) error
}, origin string, ttl time.Duration) (string, error) {
	token := webauth.RandomToken()
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "insert into web_setup(token_hash, expires_at, created_at) values(?,?,?)",
			webauth.Hash(token), time.Now().Add(ttl).UnixMilli(), ids.NowMs())
		if err == nil {
			err = policy.Audit(ctx, tx, "WEB_SETUP_LINK_CREATED", "owner", "", "", map[string]any{"expires_in_s": int(ttl.Seconds())})
		}
		return err
	})
	// The token travels in the fragment: browsers never send it to a server or a log.
	return strings.TrimRight(origin, "/") + "/app/#/setup/" + token, err
}

var _ = json.Marshal
var _ = vault.RefPrefix

type webCtxKey struct{}

var webKey = webCtxKey{}

// viaWeb reports whether the request was authorized by a Web App session.
func viaWeb(r *http.Request) bool { v, _ := r.Context().Value(webKey).(bool); return v }

// stepUp verifies a fresh TOTP code for a consequential web action; each code works once.
func (s *Server) stepUp(r *http.Request, code string) bool {
	if s.Vault == nil {
		return false
	}
	secret, err := s.Vault.Get(r.Context(), s.Store.R, totpSecretKey)
	if err != nil {
		return false
	}
	var last int64
	s.Store.R.QueryRowContext(r.Context(), "select totp_last_step from web_owner where id=1").Scan(&last)
	step, ok := webauth.VerifyTOTP(secret, code, time.Now(), last)
	if !ok {
		return false
	}
	return s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "update web_owner set totp_last_step=? where id=1 and totp_last_step < ?", step, step)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("code already used")
		}
		return nil
	}) == nil
}

// appCSP forbids inline script, remote code, framing and plugin content.
const appCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// webApp serves the embedded single-page app (Blueprint 13A.7 rule 10).
func (s *Server) webApp(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/app/")
	if name == "" {
		name = "index.html"
	}
	b, err := fs.ReadFile(webui.FS(), name)
	if err != nil || strings.Contains(name, "/") {
		s.fail(w, r, 404, "not_found", "no such file")
		return
	}
	ct := map[string]string{".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}[path.Ext(name)]
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", appCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Strict-Transport-Security", "max-age=31536000")
	w.Write(b)
}
