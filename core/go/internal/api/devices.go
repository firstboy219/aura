package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"aura.local/core/internal/device"
)

// Device endpoints (ODI A6, ADR-0075). Owner endpoints use the normal auth; pairing completion is public but needs the one-time challenge and a proof of key
// possession; everything a paired device does is ed25519-signed (X-Aura-Device, X-Aura-Timestamp (ms), X-Aura-Nonce, X-Aura-Signature).

func (s *Server) deviceErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, device.ErrPairing):
		s.fail(w, r, 403, "pairing_invalid", "the pairing is invalid, expired or already used")
	case errors.Is(err, device.ErrNotTrusted):
		s.fail(w, r, 403, "device_not_trusted", "the device is not trusted")
	case errors.Is(err, device.ErrBadSignature), errors.Is(err, device.ErrStale):
		s.fail(w, r, 401, "device_auth_failed", "signature invalid or request stale")
	case errors.Is(err, device.ErrReplay):
		s.fail(w, r, 401, "request_replayed", "nonce already used")
	case errors.Is(err, device.ErrCapability):
		s.fail(w, r, 403, "capability_not_granted", err.Error())
	case errors.Is(err, device.ErrLeaseConflict):
		s.fail(w, r, 409, "lease_conflict", "the task is already under a takeover lease, or the lease is not yours or not active")
	case errors.Is(err, device.ErrNeedsObserve):
		s.fail(w, r, 422, "fresh_observation_required", "hand back with the sha256 of a fresh observation")
	default:
		s.mapErr(w, r, err)
	}
}

func (s *Server) ownerOnly(w http.ResponseWriter, r *http.Request) bool {
	if p := PrincipalOf(r); p.Role != RoleOwner {
		s.fail(w, r, 403, "forbidden", "only the owner manages devices")
		return false
	}
	return true
}

func (s *Server) startPairing(w http.ResponseWriter, r *http.Request) {
	if s.Devices == nil || !s.ownerOnly(w, r) {
		return
	}
	var in struct {
		Capabilities []string `json:"capabilities"`
		TTLMinutes   int      `json:"ttl_minutes"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	id, ch, exp, err := s.Devices.StartPairing(r.Context(), "owner", in.Capabilities, time.Duration(in.TTLMinutes)*time.Minute)
	if err != nil {
		s.fail(w, r, 400, "invalid_input", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "challenge": ch, "expires_at": ts(exp), "note": "the challenge is shown once; enter it on the device"})
}

func (s *Server) completePairing(w http.ResponseWriter, r *http.Request) {
	if s.Devices == nil {
		s.fail(w, r, 404, "not_found", "devices are not configured")
		return
	}
	if !s.loginLim.allow(s.clientIP(r)) {
		s.fail(w, r, 429, "rate_limited", "too many attempts; wait a minute")
		return
	}
	var in device.PairRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		s.fail(w, r, 400, "invalid_input", "invalid pairing request")
		return
	}
	d, err := s.Devices.CompletePairing(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.deviceErr(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"device_id": d.ID, "key_id": d.KeyID, "trust": d.Trust, "next": "waiting for the owner to confirm this device and its capabilities"})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	if s.Devices == nil || !s.ownerOnly(w, r) {
		return
	}
	ds, err := s.Devices.List(r.Context())
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"devices": ds})
}

func (s *Server) confirmDevice(w http.ResponseWriter, r *http.Request) {
	if s.Devices == nil || !s.ownerOnly(w, r) {
		return
	}
	var in struct {
		Capabilities []string `json:"capabilities"`
		Code         string   `json:"code"` // web: step-up TOTP code, as for approvals
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	if viaWeb(r) && !s.stepUp(r, in.Code) {
		s.fail(w, r, 401, "step_up_required", "enter a current code from your authenticator app to trust a device")
		return
	}
	d, err := s.Devices.Confirm(r.Context(), r.PathValue("id"), "owner", in.Capabilities)
	if err != nil {
		s.deviceErr(w, r, err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	if s.Devices == nil || !s.ownerOnly(w, r) {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	tasks, err := s.Devices.Revoke(r.Context(), r.PathValue("id"), "owner", truncateStr(in.Reason, 300))
	if err != nil {
		s.deviceErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true, "tasks_left_paused": tasks, "note": "tasks the device held stay paused until you decide"})
}

// signedDevice authenticates a request from a paired device and hands the verified device and the exact body to next.
func (s *Server) signedDevice(next func(http.ResponseWriter, *http.Request, *device.Device, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Devices == nil {
			s.fail(w, r, 404, "not_found", "devices are not configured")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			s.fail(w, r, 400, "invalid_input", "body too large")
			return
		}
		tsMs, _ := strconv.ParseInt(r.Header.Get("X-Aura-Timestamp"), 10, 64)
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Aura-Signature"))
		if len(sig) != ed25519.SignatureSize {
			s.fail(w, r, 401, "device_auth_failed", "signature invalid or request stale")
			return
		}
		d, err := s.Devices.Authenticate(r.Context(), r.Header.Get("X-Aura-Device"), r.Method, r.URL.Path, tsMs, r.Header.Get("X-Aura-Nonce"), body, sig)
		if err != nil {
			s.deviceErr(w, r, err)
			return
		}
		next(w, r, d, body)
	}
}

func (s *Server) deviceHeartbeat(w http.ResponseWriter, r *http.Request, d *device.Device, body []byte) {
	writeJSON(w, 200, map[string]any{"device_id": d.ID, "trust": d.Trust, "capabilities": d.Capabilities, "server_time": ts(time.Now().UnixMilli())})
}

func (s *Server) deviceBeginLease(w http.ResponseWriter, r *http.Request, d *device.Device, body []byte) {
	var in struct {
		TaskID     string `json:"task_id"`
		TTLMinutes int    `json:"ttl_minutes"`
	}
	if err := json.Unmarshal(body, &in); err != nil || in.TaskID == "" {
		s.fail(w, r, 400, "invalid_input", "body must be {\"task_id\": \"...\", \"ttl_minutes\": n}")
		return
	}
	l, err := s.Devices.BeginLease(r.Context(), d, in.TaskID, time.Duration(in.TTLMinutes)*time.Minute, s.Engine.PauseForLease)
	if err != nil {
		s.deviceErr(w, r, err)
		return
	}
	writeJSON(w, 201, l)
}

func (s *Server) deviceReleaseLease(w http.ResponseWriter, r *http.Request, d *device.Device, body []byte) {
	var in struct {
		Observation string `json:"observation_sha256"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		s.fail(w, r, 400, "invalid_input", "body must be {\"observation_sha256\": \"<64 hex>\"}")
		return
	}
	l, err := s.Devices.ReleaseLease(r.Context(), d, r.PathValue("id"), in.Observation)
	if err != nil {
		s.deviceErr(w, r, err)
		return
	}
	resumed := false
	if l.Paused {
		resumed = s.Engine.ResumeAfterLease(r.Context(), l.TaskID) == nil
	}
	writeJSON(w, 200, map[string]any{"lease": l, "task_resumed": resumed})
}
