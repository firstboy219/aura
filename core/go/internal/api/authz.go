package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// Server-side authorization by role (Blueprint 13A.7 rule 2, D.1 P5 Workspace
// Admin foundation, ADR-0043). Every authenticated route has a minimum role;
// hiding a UI element is never the control.

// Role levels, lowest first.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleApprover = "approver"
	RoleOwner    = "owner"
)

var roleLevel = map[string]int{RoleViewer: 1, RoleOperator: 2, RoleApprover: 3, RoleOwner: 4}

// Principal is who made a request.
type Principal struct {
	ID, Name, Role string
}

type principalKey struct{}

// PrincipalOf returns the request's principal (owner if unset: internal callers).
func PrincipalOf(r *http.Request) Principal {
	if p, ok := r.Context().Value(principalKey{}).(Principal); ok {
		return p
	}
	return Principal{ID: "owner", Name: "owner", Role: RoleOwner}
}

func withPrincipal(r *http.Request, p Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

// routeRole overrides the default (GET → viewer, other methods → operator).
var routeRole = map[string]string{
	"POST /api/v1/approvals/{id}/{decision}":      RoleApprover,
	"POST /api/v1/spatial/sessions":               RoleApprover, // authorizing capture of the owner's surroundings is a trusted act
	"POST /api/v1/spatial/sessions/{id}/{action}": RoleApprover,
	"GET /api/v1/admin/egress":                    RoleOwner,
	"GET /api/v1/admin/retention":                 RoleOwner,
	"PUT /api/v1/admin/retention":                 RoleOwner,
	"POST /api/v1/admin/retention/purge":          RoleOwner,
	"GET /api/v1/admin/spatial":                   RoleOwner,
	"PUT /api/v1/admin/spatial":                   RoleOwner,
	"POST /api/v1/system/stop":                    RoleApprover, // the kill switch: anyone trusted to approve may stop everything
	"POST /api/v1/limitations/{id}/{action}":      RoleApprover,
	// Owner only: credentials, sessions, tools, evolution, deletion, admin.
	"POST /api/v1/tools/{id}/{action}":                   RoleOwner,
	"POST /api/v1/brain/variants/{id}/{action}":          RoleOwner,
	"POST /api/v1/workflows":                             RoleOwner,
	"POST /api/v1/workflows/{id}/disable":                RoleOwner,
	"DELETE /api/v1/memories/{id}":                       RoleOwner,
	"POST /api/v1/skills/{id}/{action}":                  RoleOwner,
	"POST /api/v1/skills/import":                         RoleOwner,
	"POST /api/v1/improvement/run":                       RoleOwner,
	"POST /api/v1/browser/check":                         RoleOwner,
	"GET /api/v1/browser/profiles":                       RoleOwner,
	"POST /api/v1/browser/profiles/{name}/session":       RoleOwner,
	"DELETE /api/v1/browser/profiles/{name}/session":     RoleOwner,
	"POST /api/v1/browser/profiles/{name}/login":         RoleOwner,
	"POST /api/v1/browser/profiles/{name}/login/finish":  RoleOwner,
	"GET /api/v1/browser/sessions":                       RoleOwner,
	"GET /api/v1/browser/sessions/{key}/screen":          RoleOwner,
	"POST /api/v1/browser/sessions/{key}/input":          RoleOwner,
	"POST /api/v1/browser/sessions/{key}/return":         RoleOwner,
	"POST /api/v1/tasks/{id}/browser/takeover":           RoleOwner,
	"POST /api/v1/tasks/{id}/browser-code":               RoleOwner,
	"GET /api/v1/admin/members":                          RoleOwner,
	"GET /api/v1/admin/app-accounts":                     RoleOwner,
	"PUT /api/v1/admin/app-accounts/{username}":          RoleOwner,
	"POST /api/v1/admin/app-accounts/{username}/disable": RoleOwner,
	"POST /api/v1/app/logout":                            RoleViewer, // any device may sign itself out
	"POST /api/v1/admin/members":                         RoleOwner,
	"POST /api/v1/admin/members/{id}/disable":            RoleOwner,
	"POST /api/v1/admin/members/{id}/department":         RoleOwner,
	"GET /api/v1/admin/audit":                            RoleOwner,
	"GET /api/v1/admin/usage":                            RoleOwner,
	"GET /api/v1/admin/budget":                           RoleOwner,
	"PUT /api/v1/admin/budget":                           RoleOwner,
	"GET /api/v1/admin/credentials":                      RoleOwner,
	"PUT /api/v1/admin/credentials/{name}":               RoleOwner,
	"DELETE /api/v1/admin/credentials/{name}":            RoleOwner,
}

// requiredRole is the minimum role for a matched route pattern.
func requiredRole(pattern string) string {
	if r, ok := routeRole[pattern]; ok {
		return r
	}
	if strings.HasPrefix(pattern, "GET ") {
		return RoleViewer
	}
	return RoleOperator
}

func allowed(role, pattern string) bool { return roleLevel[role] >= roleLevel[requiredRole(pattern)] }

func tokenHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// memberByToken resolves a member's bearer token (disabled members are refused).
func (s *Server) memberByToken(r *http.Request, tok string) (Principal, bool) {
	if !strings.HasPrefix(tok, "aura_m_") {
		return Principal{}, false
	}
	var p Principal
	err := s.Store.R.QueryRowContext(r.Context(), "select id, name, role from members where token_hash=? and disabled_at is null", tokenHash(tok)).
		Scan(&p.ID, &p.Name, &p.Role)
	if err != nil {
		return Principal{}, false
	}
	s.Store.Write(context.WithoutCancel(r.Context()), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), "update members set last_used_at=? where id=?", time.Now().UnixMilli(), p.ID)
		return err
	})
	return p, true
}
