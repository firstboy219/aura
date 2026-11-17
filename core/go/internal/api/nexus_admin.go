package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"aura.local/core/internal/nexus"
	"aura.local/core/internal/policy"
)

// Admin endpoints for the ODI NEXUS gateway (ADR-0073). The owner is the only one who can change the mode, name a fallback or Teacher provider, or
// switch NEXUS off; every change is audited.

func (s *Server) nexusStatus(ctx context.Context) map[string]any {
	p, _ := nexus.LoadPolicy(ctx, s.Store.R)
	st, ev := s.Nexus.Mon.State()
	var shadowN, shadowOK int
	var avg sql.NullFloat64
	s.Store.R.QueryRowContext(ctx, "select count(*), coalesce(sum(ok),0), avg(nexus_ms) from nexus_shadow where created_at>=?", time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&shadowN, &shadowOK, &avg)
	active, _ := s.Nexus.Auth.Active(ctx)
	return map[string]any{"teaching": s.Nexus.Router.TeachingCounts(), "policy": p, "health": map[string]any{"state": string(st), "evidence": ev}, "active_fallbacks": active,
		"shadow_24h": map[string]any{"calls": shadowN, "ok": shadowOK, "avg_nexus_ms": avg.Float64}}
}

func (s *Server) getNexus(w http.ResponseWriter, r *http.Request) {
	if s.Nexus == nil {
		s.fail(w, r, 404, "not_found", "NEXUS gateway not configured")
		return
	}
	writeJSON(w, 200, s.nexusStatus(r.Context()))
}

// putNexus merges the given fields into the policy. Switching to primary is gated: NEXUS must answer a readiness probe now.
func (s *Server) putNexus(w http.ResponseWriter, r *http.Request) {
	if s.Nexus == nil {
		s.fail(w, r, 404, "not_found", "NEXUS gateway not configured")
		return
	}
	cur, err := nexus.LoadPolicy(r.Context(), s.Store.R)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	next := cur
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		s.fail(w, r, 400, "invalid_input", "body must be a JSON object of policy fields: "+err.Error())
		return
	}
	next.OwnerDisabledUntil = cur.OwnerDisabledUntil // changed only through owner-disable
	if err := next.Validate(); err != nil {
		s.fail(w, r, 400, "invalid_input", err.Error())
		return
	}
	if next.Mode == nexus.ModePrimary && cur.Mode != nexus.ModePrimary {
		if err := s.Nexus.ActivationCheck(r.Context(), next.BaseURL); err != nil {
			s.fail(w, r, 409, "NEXUS_HEALTH_UNKNOWN", "NEXUS must answer consecutive readiness probes before it becomes primary: "+err.Error())
			return
		}
	}
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if err := nexus.SavePolicy(r.Context(), tx, next, time.Now()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "NEXUS_POLICY_CHANGED", "owner", "", requestID(r), map[string]any{"mode": map[string]any{"from": cur.Mode, "to": next.Mode},
			"base_url": next.BaseURL, "fallback_providers": next.FallbackProviders, "fallback_data_classes": next.FallbackDataClass, "fallback_budget_usd": next.FallbackBudgetUSD,
			"fallback_max_calls": next.FallbackMaxCalls, "teacher_enabled": next.TeacherEnabled, "teacher_providers": next.TeacherProviders, "teacher_max_per_day": next.TeacherMaxPerDay})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, s.nexusStatus(r.Context()))
}

// nexusOwnerDisable is the owner's explicit switch-off (the second valid reason for fallback): minutes 1..1440; 0 switches NEXUS back on and revokes
// every fallback authorization.
func (s *Server) nexusOwnerDisable(w http.ResponseWriter, r *http.Request) {
	if s.Nexus == nil {
		s.fail(w, r, 404, "not_found", "NEXUS gateway not configured")
		return
	}
	var in struct {
		Minutes int `json:"minutes"`
	}
	if err := decodeStrict(r, &in); err != nil || in.Minutes < 0 || in.Minutes > 1440 {
		s.fail(w, r, 400, "invalid_input", "minutes must be 0 (switch NEXUS back on) .. 1440")
		return
	}
	if p := PrincipalOf(r); p.Role != RoleOwner {
		s.fail(w, r, 403, "forbidden", "only the owner can switch NEXUS off")
		return
	}
	cur, err := nexus.LoadPolicy(r.Context(), s.Store.R)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	next := cur
	next.OwnerDisabledUntil = 0
	if in.Minutes > 0 {
		next.OwnerDisabledUntil = time.Now().Add(time.Duration(in.Minutes) * time.Minute).UnixMilli()
	}
	err = s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		if err := nexus.SavePolicy(r.Context(), tx, next, time.Now()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "NEXUS_OWNER_DISABLE", "owner", "", requestID(r), map[string]any{"minutes": in.Minutes, "until_ms": next.OwnerDisabledUntil})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if in.Minutes == 0 {
		s.Nexus.Auth.RevokeAll(r.Context(), "OWNER_ENABLED_NEXUS")
	}
	writeJSON(w, 200, s.nexusStatus(r.Context()))
}

// teacherRequest is NEXUS asking to learn from an external AI (Teacher Gate). It has its own service-token check: the token can do nothing else.
func (s *Server) teacherRequest(w http.ResponseWriter, r *http.Request) {
	if s.Nexus == nil || s.Vault == nil {
		s.fail(w, r, 404, "not_found", "Teacher Gate not configured")
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	want, err := s.Vault.Get(r.Context(), s.Store.R, "connector.teacher_service_token")
	if err != nil || want == "" || len(tok) < 20 || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		s.Store.Write(context.WithoutCancel(r.Context()), func(tx *sql.Tx) error {
			return policy.Audit(r.Context(), tx, "TEACHER_AUTH_FAILED", "nexus", "", requestID(r), map[string]any{"remote": s.clientIP(r)})
		})
		s.fail(w, r, 401, "unauthorized", "a valid Teacher Gate service token is required")
		return
	}
	var in nexus.TeacherInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		s.fail(w, r, 400, "invalid_input", "body must be {gap, question, expected_form}")
		return
	}
	ans, err := s.Nexus.Teacher.Ask(r.Context(), in)
	var denied *nexus.TeacherDenied
	switch {
	case errors.As(err, &denied):
		code := 403
		if denied.Code == "TEACHER_UNAVAILABLE" {
			code = 502
		} else if denied.Code == "INVALID_REQUEST" {
			code = 400
		}
		writeJSON(w, code, map[string]any{"error": map[string]any{"code": denied.Code, "message": denied.Why, "request_id": requestID(r)}})
	case err != nil:
		s.mapErr(w, r, err)
	default:
		writeJSON(w, 200, ans)
	}
}
