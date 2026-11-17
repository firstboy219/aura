package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
)

// Spatial session authorization (ADR-0081, JARVIS blueprint v1.4 §6, §14, §16). AURA authorizes the scope; AMPI perceives; JARVIS presents. This file stores no frames, no precise location and
// no scene content, and it never calls AMPI or NEXUS: version 1 only decides and records what a session may be, and refuses everything riskier until the owner approves it separately.

const (
	spatialSettingKey = "spatial.sessions_enabled"
	spatialMinSecs    = 30
	spatialMaxSecs    = 30 * 60
	spatialDefaultSec = 10 * 60
	spatialMaxDevice  = 2048
)

var (
	spatialPurposes   = map[string]bool{"map_assist": true, "static_image_analysis": true} // live_camera is a later phase (SP-J4)
	spatialModalities = map[string]bool{"image": true, "location": true}                   // video, audio, depth and motion are later phases
	spatialRefused    = map[string]string{"room_mapping": "room mapping needs a separate owner approval and a privacy review", "persistent_anchor": "persistent anchors are refused (ADR-021: session only)", "shared_anchor": "shared anchors are refused (ADR-021)", "geographic_anchor": "geographic anchors are refused (ADR-021)", "bystanders": "bystander capture needs a separate owner approval and a privacy review", "background": "background capture is refused"}
)

// SpatialRequest is what JARVIS sends: names and choices only.
type SpatialRequest struct {
	Purpose        string         `json:"purpose"`
	Modalities     []string       `json:"modalities"`
	CaptureScope   []string       `json:"capture_scope"`
	DeviceManifest map[string]any `json:"device_manifest"`
	DurationS      int            `json:"duration_s"`
	Retention      string         `json:"retention_preference"`
	ConsentRef     string         `json:"consent_ref"`
	Foreground     bool           `json:"foreground"`
}

// spatialVerdict is the pure decision: nil Err means authorized as asked (with defaults filled).
type spatialVerdict struct {
	Status int
	Code   string
	Msg    string
}

// decideSpatial validates a request. Pure: no clock, no database, so every refusal is tested by name.
func decideSpatial(q *SpatialRequest) *spatialVerdict {
	deny := func(st int, code, msg string) *spatialVerdict { return &spatialVerdict{st, code, msg} }
	if !spatialPurposes[q.Purpose] {
		if q.Purpose == "live_camera" {
			return deny(422, "capability_unsupported", "live camera sessions are a later phase (SP-J4)")
		}
		return deny(400, "invalid_input", "purpose must be map_assist or static_image_analysis")
	}
	if len(q.Modalities) == 0 || len(q.Modalities) > 4 {
		return deny(400, "invalid_input", "modalities are required")
	}
	for _, m := range q.Modalities {
		if !spatialModalities[m] {
			return deny(422, "capability_unsupported", "modality "+strconv.Quote(m)+" is not supported in this version (image, location only)")
		}
	}
	if !q.Foreground {
		return deny(403, "permission_denied", spatialRefused["background"])
	}
	for _, sc := range q.CaptureScope {
		if why, bad := spatialRefused[sc]; bad {
			return deny(403, "permission_denied", why)
		}
	}
	if len(q.CaptureScope) > 8 {
		return deny(400, "invalid_input", "too many scope labels")
	}
	if q.Retention == "" {
		q.Retention = "session_ephemeral"
	}
	if q.Retention != "session_ephemeral" {
		return deny(403, "permission_denied", "only session_ephemeral retention is authorized in this version")
	}
	if len(q.ConsentRef) < 3 || len(q.ConsentRef) > 120 {
		return deny(403, "consent_expired", "a consent reference is required: the owner must confirm the purpose, scope and retention first")
	}
	if q.DurationS == 0 {
		q.DurationS = spatialDefaultSec
	}
	if q.DurationS < spatialMinSecs || q.DurationS > spatialMaxSecs {
		return deny(400, "invalid_input", "duration_s must be between 30 and 1800")
	}
	if len(q.DeviceManifest) > 40 {
		return deny(400, "invalid_input", "device_manifest has too many entries")
	}
	for k, v := range q.DeviceManifest {
		switch x := v.(type) {
		case bool, float64, nil:
		case string:
			if len(x) > 40 {
				return deny(400, "invalid_input", "device_manifest values must be short: names and booleans only ("+k+")")
			}
		default:
			return deny(400, "invalid_input", "device_manifest values must be scalars ("+k+")")
		}
	}
	return nil
}

func spatialEnabled(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) bool {
	var v string
	if q.QueryRowContext(ctx, "select value from settings where key=?", spatialSettingKey).Scan(&v) != nil {
		return false // absent = off
	}
	return v == "1"
}

type spatialSession struct {
	ID            string          `json:"session_id"`
	Purpose       string          `json:"purpose"`
	Modalities    json.RawMessage `json:"modalities"`
	CaptureScope  json.RawMessage `json:"capture_scope"`
	Retention     string          `json:"retention_class"`
	Foreground    bool            `json:"foreground"`
	State         string          `json:"state"`
	StateReason   *string         `json:"state_reason"`
	CreatedAt     int64           `json:"created_at"`
	ExpiresAt     int64           `json:"expires_at"`
	EndedAt       *int64          `json:"ended_at,omitempty"`
	AMPISessRef   *string         `json:"ampi_session_ref"`
	AMPIState     string          `json:"ampi_state"`
	PolicyResult  string          `json:"policy_result,omitempty"`
	ConsentRef    string          `json:"consent_ref,omitempty"`
	NotesForOwner string          `json:"note,omitempty"`
}

const spatialCols = "id, purpose, modalities, capture_scope, retention_class, foreground, state, state_reason, created_at, expires_at, ended_at, ampi_session_ref, ampi_state, consent_ref"

func scanSpatial(sc interface{ Scan(...any) error }) (*spatialSession, error) {
	var x spatialSession
	var mod, scope string
	var fg int
	if err := sc.Scan(&x.ID, &x.Purpose, &mod, &scope, &x.Retention, &fg, &x.State, &x.StateReason, &x.CreatedAt, &x.ExpiresAt, &x.EndedAt, &x.AMPISessRef, &x.AMPIState, &x.ConsentRef); err != nil {
		return nil, err
	}
	x.Modalities, x.CaptureScope, x.Foreground = json.RawMessage(mod), json.RawMessage(scope), fg == 1
	return &x, nil
}

// expireSpatial marks overdue authorized sessions expired (the session simply ends; nothing is resumed).
func (s *Server) expireSpatial(ctx context.Context) {
	now := time.Now().UnixMilli()
	s.Store.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update spatial_sessions set state='expired', state_reason='time_limit', ended_at=? where state='authorized' and expires_at<=?", now, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return policy.Audit(ctx, tx, "SPATIAL_SESSION_EXPIRED", "system", "", "", map[string]any{"count": n})
		}
		return nil
	})
}

func (s *Server) spatialGate(w http.ResponseWriter, r *http.Request) bool {
	if !spatialEnabled(r.Context(), s.Store.R) {
		s.fail(w, r, 403, "spatial_disabled", "spatial sessions are not enabled; the owner turns them on (PUT /api/v1/admin/spatial)")
		return false
	}
	return true
}

// createSpatial authorizes a scoped spatial session. It does not start a capture and does not contact AMPI.
func (s *Server) createSpatial(w http.ResponseWriter, r *http.Request) {
	if !s.spatialGate(w, r) {
		return
	}
	var q SpatialRequest
	if err := decodeStrict(r, &q); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if v := decideSpatial(&q); v != nil {
		s.Store.Write(context.WithoutCancel(r.Context()), func(tx *sql.Tx) error {
			return policy.Audit(r.Context(), tx, "SPATIAL_SESSION_DENIED", PrincipalOf(r).Name, "", requestID(r), map[string]any{"purpose": q.Purpose, "code": v.Code})
		})
		s.fail(w, r, v.Status, v.Code, v.Msg)
		return
	}
	s.expireSpatial(r.Context())
	now := time.Now()
	id := "sps-" + ids.New()
	mod, _ := json.Marshal(q.Modalities)
	scope, _ := json.Marshal(append([]string{}, q.CaptureScope...))
	man, _ := json.Marshal(q.DeviceManifest)
	if len(man) > spatialMaxDevice {
		s.fail(w, r, 400, "invalid_input", "device_manifest is larger than 2 KiB")
		return
	}
	var conflict bool
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(r.Context(), "select count(*) from spatial_sessions where state='authorized' and expires_at>?", now.UnixMilli()).Scan(&n)
		if n > 0 {
			conflict = true // one spatial session at a time: nothing silently stacks
			return nil
		}
		if _, err := tx.ExecContext(r.Context(), `insert into spatial_sessions(id, purpose, modalities, capture_scope, device_manifest, retention_class, consent_ref, foreground, state, requested_by, created_at, expires_at)
			values(?,?,?,?,?,?,?,1,'authorized',?,?,?)`, id, q.Purpose, string(mod), string(scope), string(man), q.Retention, q.ConsentRef, PrincipalOf(r).Name, now.UnixMilli(), now.Add(time.Duration(q.DurationS)*time.Second).UnixMilli()); err != nil {
			return err
		}
		return policy.Audit(r.Context(), tx, "SPATIAL_SESSION_AUTHORIZED", PrincipalOf(r).Name, "", requestID(r), map[string]any{"session": id, "purpose": q.Purpose, "modalities": q.Modalities, "retention": q.Retention, "duration_s": q.DurationS})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	if conflict {
		s.fail(w, r, 409, "conflict", "a spatial session is already active; stop it first")
		return
	}
	x, err := scanSpatial(s.Store.R.QueryRowContext(r.Context(), "select "+spatialCols+" from spatial_sessions where id=?", id))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	x.PolicyResult = "authorized"
	x.NotesForOwner = "AURA authorized the scope only. AMPI is not connected to spatial sessions yet (no client credential): no frame can be uploaded and nothing is being captured."
	writeJSON(w, 201, x)
}

func (s *Server) getSpatial(w http.ResponseWriter, r *http.Request) {
	if !s.spatialGate(w, r) {
		return
	}
	s.expireSpatial(r.Context())
	x, err := scanSpatial(s.Store.R.QueryRowContext(r.Context(), "select "+spatialCols+" from spatial_sessions where id=?", r.PathValue("id")))
	if err != nil {
		s.fail(w, r, 404, "not_found", "no such spatial session")
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) listSpatial(w http.ResponseWriter, r *http.Request) {
	if !s.spatialGate(w, r) {
		return
	}
	s.expireSpatial(r.Context())
	rows, err := s.Store.R.QueryContext(r.Context(), "select "+spatialCols+" from spatial_sessions order by created_at desc limit 20")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	defer rows.Close()
	out := []*spatialSession{}
	for rows.Next() {
		x, err := scanSpatial(rows)
		if err != nil {
			s.mapErr(w, r, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

// endSpatial stops (the owner is done) or revokes (consent withdrawn) an authorized session. Both are immediate and final: an ended session is never resumed.
func (s *Server) endSpatial(w http.ResponseWriter, r *http.Request) {
	if !s.spatialGate(w, r) {
		return
	}
	action := r.PathValue("action")
	state := map[string]string{"stop": "stopped", "revoke": "revoked"}[action]
	if state == "" {
		s.fail(w, r, 404, "not_found", "no such action")
		return
	}
	id := r.PathValue("id")
	var changed bool
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), "update spatial_sessions set state=?, state_reason=?, ended_at=? where id=? and state='authorized'", state, "owner_"+action, time.Now().UnixMilli(), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		changed = n == 1
		if changed {
			return policy.Audit(r.Context(), tx, "SPATIAL_SESSION_"+map[string]string{"stop": "STOPPED", "revoke": "REVOKED"}[action], PrincipalOf(r).Name, "", requestID(r), map[string]any{"session": id})
		}
		return nil
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	x, gerr := scanSpatial(s.Store.R.QueryRowContext(r.Context(), "select "+spatialCols+" from spatial_sessions where id=?", id))
	if gerr != nil {
		s.fail(w, r, 404, "not_found", "no such spatial session")
		return
	}
	if !changed {
		s.fail(w, r, 409, "conflict", "the session is already "+x.State)
		return
	}
	writeJSON(w, 200, x)
}

// spatialAdmin: the owner's switch. GET shows it; PUT {"enabled":true|false} flips it (audited). Turning it off also ends every authorized session.
func (s *Server) getSpatialAdmin(w http.ResponseWriter, r *http.Request) {
	var n int
	s.Store.R.QueryRowContext(r.Context(), "select count(*) from spatial_sessions where state='authorized' and expires_at>?", time.Now().UnixMilli()).Scan(&n)
	writeJSON(w, 200, map[string]any{"enabled": spatialEnabled(r.Context(), s.Store.R), "active_sessions": n, "limits": map[string]any{"max_duration_s": spatialMaxSecs, "purposes": []string{"map_assist", "static_image_analysis"}, "modalities": []string{"image", "location"}, "retention": "session_ephemeral"}})
}

func (s *Server) putSpatialAdmin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	val := "0"
	if in.Enabled {
		val = "1"
	}
	err := s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(r.Context(), `insert into settings(key, value, updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, spatialSettingKey, val, now); err != nil {
			return err
		}
		if !in.Enabled {
			if _, err := tx.ExecContext(r.Context(), "update spatial_sessions set state='revoked', state_reason='feature_disabled', ended_at=? where state='authorized'", now); err != nil {
				return err
			}
		}
		return policy.Audit(r.Context(), tx, "SPATIAL_FLAG_CHANGED", PrincipalOf(r).Name, "", requestID(r), map[string]any{"enabled": in.Enabled})
	})
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.getSpatialAdmin(w, r)
}
