// Package device is AURA's device registry (ODI AURA Blueprint v1.1 §13, ADR-0075): pairing by short-lived challenge with proof of key possession and explicit
// owner confirmation and capability consent; ed25519-signed requests with replay protection; revocation that stops the device immediately; and takeover leases.
// No desktop worker exists yet; this is the server side of that contract.
package device

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
)

// Capabilities a device may be consented to. PROPOSED set for the paired local desktop worker (screen observation and input are narrowly separated).
var AllowedCapabilities = []string{"screen.observe", "input.mouse", "input.keyboard", "files.read", "files.write", "browser.control", "notifications"}

// Errors with stable meanings.
var (
	ErrPairing       = errors.New("pairing_invalid") // wrong, expired or already used challenge, or a bad proof of key possession
	ErrNotTrusted    = errors.New("device_not_trusted")
	ErrBadSignature  = errors.New("signature_invalid")
	ErrReplay        = errors.New("request_replayed")
	ErrStale         = errors.New("request_stale")
	ErrCapability    = errors.New("capability_not_granted")
	ErrLeaseConflict = errors.New("lease_conflict")
	ErrNeedsObserve  = errors.New("fresh_observation_required")
)

const (
	MaxClockSkew = 60 * time.Second
	nonceKeep    = 5 * time.Minute
)

type Device struct {
	ID           string   `json:"id"`
	OwnerRef     string   `json:"owner_ref"`
	Name         string   `json:"name"`
	Platform     string   `json:"platform"`
	KeyID        string   `json:"key_id"`
	PublicKey    string   `json:"public_key"`
	Capabilities []string `json:"capabilities"`
	Requested    []string `json:"requested"`
	Trust        string   `json:"trust"`
	AppVersion   string   `json:"app_version,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	PairedAt     *int64   `json:"paired_at,omitempty"`
	LastSeenAt   *int64   `json:"last_seen_at,omitempty"`
	RevokedAt    *int64   `json:"revoked_at,omitempty"`
	RevokeReason string   `json:"revoke_reason,omitempty"`
}

type Service struct {
	St  *store.Store
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func hashHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// KeyID is the non-secret identifier of a public key.
func KeyID(pub []byte) string { return hashHex(pub)[:16] }

func validCaps(caps []string) ([]string, error) {
	if len(caps) == 0 {
		return nil, errors.New("at least one capability is required")
	}
	out := []string{}
	for _, c := range caps {
		if !slices.Contains(AllowedCapabilities, c) {
			return nil, fmt.Errorf("unknown capability %q", c)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out, nil
}

// StartPairing creates a pairing and returns its one-time challenge (never stored in clear).
func (s *Service) StartPairing(ctx context.Context, ownerRef string, caps []string, ttl time.Duration) (id, challenge string, expires int64, err error) {
	caps, err = validCaps(caps)
	if err != nil {
		return "", "", 0, err
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		ttl = 5 * time.Minute
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	challenge = base64.RawURLEncoding.EncodeToString(raw)
	id, expires = ids.New(), s.now().Add(ttl).UnixMilli()
	cj, _ := json.Marshal(caps)
	err = s.St.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into device_pairings(id, challenge_sha256, owner_ref, capabilities_json, expires_at, state, created_at) values(?,?,?,?,?, 'open', ?)`,
			id, hashHex([]byte(challenge)), ownerRef, string(cj), expires, s.now().UnixMilli()); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "DEVICE_PAIRING_STARTED", "owner", "", id, map[string]any{"owner": ownerRef, "capabilities": caps, "expires_at": expires})
	})
	return id, challenge, expires, err
}

// PairRequest is what the device sends.
type PairRequest struct {
	Challenge  string `json:"challenge"`
	PublicKey  string `json:"public_key"` // ed25519, base64
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
	Signature  string `json:"signature"` // base64 ed25519 over PairMessage: proof the device holds the private key
}

// PairMessage is what the device signs to prove it holds the key it presents.
func PairMessage(pairingID, challenge, publicKey string) []byte {
	return []byte("aura-device-pair/1\n" + pairingID + "\n" + challenge + "\n" + publicKey)
}

// CompletePairing verifies the challenge (one use, unexpired) and the proof of key possession, then registers the device as PENDING: it can do nothing
// until the owner confirms it and consents to capabilities.
func (s *Service) CompletePairing(ctx context.Context, pairingID string, req PairRequest) (*Device, error) {
	pub, err := base64.StdEncoding.DecodeString(req.PublicKey)
	sig, err2 := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil || err2 != nil || len(pub) != ed25519.PublicKeySize || req.Name == "" || len(req.Name) > 80 {
		return nil, ErrPairing
	}
	if !slices.Contains([]string{"android", "web", "desktop", "other"}, req.Platform) {
		return nil, ErrPairing
	}
	var d *Device
	err = s.St.Write(ctx, func(tx *sql.Tx) error {
		var hash, owner, capsJSON, state string
		var exp int64
		if err := tx.QueryRowContext(ctx, "select challenge_sha256, owner_ref, capabilities_json, expires_at, state from device_pairings where id=?", pairingID).Scan(&hash, &owner, &capsJSON, &exp, &state); err != nil {
			return ErrPairing
		}
		now := s.now().UnixMilli()
		if state != "open" || now >= exp || subtle.ConstantTimeCompare([]byte(hash), []byte(hashHex([]byte(req.Challenge)))) != 1 ||
			!ed25519.Verify(pub, PairMessage(pairingID, req.Challenge, req.PublicKey), sig) {
			if state == "open" && now >= exp {
				tx.ExecContext(ctx, "update device_pairings set state='expired' where id=?", pairingID)
				return store.Commit(ErrPairing)
			}
			return ErrPairing
		}
		var caps []string
		json.Unmarshal([]byte(capsJSON), &caps)
		d = &Device{ID: ids.New(), OwnerRef: owner, Name: req.Name, Platform: req.Platform, KeyID: KeyID(pub), PublicKey: req.PublicKey, Requested: caps, Capabilities: []string{}, Trust: "pending",
			AppVersion: req.AppVersion, CreatedAt: now}
		cj, _ := json.Marshal(d.Capabilities)
		rj, _ := json.Marshal(d.Requested)
		if _, err := tx.ExecContext(ctx, `insert into devices(id, owner_ref, name, platform, key_id, public_key, capabilities_json, requested_json, trust, app_version, created_at) values(?,?,?,?,?,?,?,?, 'pending', ?, ?)`,
			d.ID, d.OwnerRef, d.Name, d.Platform, d.KeyID, d.PublicKey, string(cj), string(rj), d.AppVersion, now); err != nil {
			return ErrPairing // e.g. the same key already registered
		}
		if _, err := tx.ExecContext(ctx, "update device_pairings set state='completed', device_id=? where id=?", d.ID, pairingID); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "DEVICE_PAIRED_PENDING", "device", "", pairingID, map[string]any{"device": d.ID, "key_id": d.KeyID, "platform": d.Platform, "requested": d.Requested})
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Confirm is the owner's explicit decision: the device becomes trusted with exactly the capabilities the owner consents to (a subset of what was requested).
func (s *Service) Confirm(ctx context.Context, deviceID, ownerRef string, consent []string) (*Device, error) {
	consent, err := validCaps(consent)
	if err != nil {
		return nil, err
	}
	var d *Device
	err = s.St.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if d, err = s.get(ctx, tx, deviceID); err != nil {
			return err
		}
		if d.Trust != "pending" || d.OwnerRef != ownerRef {
			return ErrNotTrusted
		}
		for _, c := range consent {
			if !slices.Contains(d.Requested, c) {
				return fmt.Errorf("%w: %s was not requested by the device", ErrCapability, c)
			}
		}
		now := s.now().UnixMilli()
		cj, _ := json.Marshal(consent)
		if _, err := tx.ExecContext(ctx, "update devices set trust='trusted', capabilities_json=?, paired_at=? where id=?", string(cj), now, deviceID); err != nil {
			return err
		}
		d.Trust, d.Capabilities, d.PairedAt = "trusted", consent, &now
		return policy.Audit(ctx, tx, "DEVICE_CONFIRMED", "owner", "", "", map[string]any{"device": deviceID, "capabilities": consent})
	})
	return d, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Service) get(ctx context.Context, q queryer, id string) (*Device, error) {
	var d Device
	var caps, req string
	var app sql.NullString
	var why sql.NullString
	err := q.QueryRowContext(ctx, `select id, owner_ref, name, platform, key_id, public_key, capabilities_json, requested_json, trust, app_version, created_at, paired_at, last_seen_at, revoked_at, revoke_reason from devices where id=?`, id).
		Scan(&d.ID, &d.OwnerRef, &d.Name, &d.Platform, &d.KeyID, &d.PublicKey, &caps, &req, &d.Trust, &app, &d.CreatedAt, &d.PairedAt, &d.LastSeenAt, &d.RevokedAt, &why)
	if err != nil {
		return nil, ErrNotTrusted
	}
	json.Unmarshal([]byte(caps), &d.Capabilities)
	json.Unmarshal([]byte(req), &d.Requested)
	d.AppVersion, d.RevokeReason = app.String, why.String
	return &d, nil
}

// Get loads a device.
func (s *Service) Get(ctx context.Context, id string) (*Device, error) { return s.get(ctx, s.St.R, id) }

// List returns all devices, newest first.
func (s *Service) List(ctx context.Context) ([]*Device, error) {
	rows, err := s.St.R.QueryContext(ctx, "select id from devices order by created_at desc limit 200")
	if err != nil {
		return nil, err
	}
	var idsList []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		idsList = append(idsList, id)
	}
	rows.Close()
	out := []*Device{}
	for _, id := range idsList {
		if d, err := s.Get(ctx, id); err == nil {
			out = append(out, d)
		}
	}
	return out, nil
}

// Revoke ends a device's trust at once: later signed requests fail, its active leases are revoked, and the tasks they held are returned so the caller can
// keep them paused (nothing resumes by itself). The audit row records who and why.
func (s *Service) Revoke(ctx context.Context, deviceID, by, reason string) (leasedTasks []string, err error) {
	err = s.St.Write(ctx, func(tx *sql.Tx) error {
		d, err := s.get(ctx, tx, deviceID)
		if err != nil {
			return err
		}
		if d.Trust == "revoked" {
			return nil
		}
		now := s.now().UnixMilli()
		if _, err := tx.ExecContext(ctx, "update devices set trust='revoked', revoked_at=?, revoke_reason=? where id=?", now, reason, deviceID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "select id, task_id from device_leases where device_id=? and state='active'", deviceID)
		if err != nil {
			return err
		}
		var lids []string
		for rows.Next() {
			var lid, tid string
			rows.Scan(&lid, &tid)
			lids = append(lids, lid)
			leasedTasks = append(leasedTasks, tid)
		}
		rows.Close()
		for _, lid := range lids {
			if _, err := tx.ExecContext(ctx, "update device_leases set state='revoked', released_at=? where id=?", now, lid); err != nil {
				return err
			}
		}
		return policy.Audit(ctx, tx, "DEVICE_REVOKED", "owner", "", "", map[string]any{"device": deviceID, "by": by, "reason": reason, "leases_revoked": len(lids)})
	})
	return leasedTasks, err
}

// SignMessage is what a device signs for each request: method, path, timestamp (Unix ms), nonce and the body hash.
func SignMessage(method, path string, tsMs int64, nonce string, body []byte) []byte {
	return []byte(method + "\n" + path + "\n" + strconv.FormatInt(tsMs, 10) + "\n" + nonce + "\n" + hashHex(body))
}

// Authenticate verifies one signed request from a TRUSTED device: signature, clock window and a nonce never seen before (replay).
func (s *Service) Authenticate(ctx context.Context, deviceID, method, path string, tsMs int64, nonce string, body, sig []byte) (*Device, error) {
	d, err := s.Get(ctx, deviceID)
	if err != nil || d.Trust != "trusted" {
		return nil, ErrNotTrusted
	}
	pub, _ := base64.StdEncoding.DecodeString(d.PublicKey)
	if len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, SignMessage(method, path, tsMs, nonce, body), sig) {
		return nil, ErrBadSignature
	}
	now := s.now()
	if skew := now.Sub(time.UnixMilli(tsMs)); skew > MaxClockSkew || skew < -MaxClockSkew || len(nonce) < 8 || len(nonce) > 64 {
		return nil, ErrStale
	}
	err = s.St.Write(ctx, func(tx *sql.Tx) error {
		tx.ExecContext(ctx, "delete from device_nonces where at < ?", now.Add(-nonceKeep).UnixMilli())
		if _, err := tx.ExecContext(ctx, "insert into device_nonces(device_id, nonce, at) values(?,?,?)", deviceID, nonce, now.UnixMilli()); err != nil {
			return ErrReplay
		}
		_, err := tx.ExecContext(ctx, "update devices set last_seen_at=? where id=?", now.UnixMilli(), deviceID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// Has reports whether the device was consented to a capability.
func (d *Device) Has(cap string) bool {
	return d.Trust == "trusted" && slices.Contains(d.Capabilities, cap)
}
