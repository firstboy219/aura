package device

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rig struct {
	t   *testing.T
	svc *Service
	clk *clock
	st  *store.Store
}

func newRig(t *testing.T) *rig {
	t.Helper()
	d := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(d, "a.db"), filepath.Join(d, "b"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clk := &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	return &rig{t: t, svc: &Service{St: st, Now: clk.Now}, clk: clk, st: st}
}

type keyed struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newKey() keyed { p, k, _ := ed25519.GenerateKey(rand.Reader); return keyed{p, k} }

func (k keyed) pub64() string { return base64.StdEncoding.EncodeToString(k.pub) }

// pair runs the whole pairing and confirmation; consent is what the owner allows.
func (r *rig) pair(caps, consent []string) (*Device, keyed) {
	r.t.Helper()
	ctx := context.Background()
	id, ch, _, err := r.svc.StartPairing(ctx, "owner", caps, 5*time.Minute)
	if err != nil {
		r.t.Fatal(err)
	}
	k := newKey()
	req := PairRequest{Challenge: ch, PublicKey: k.pub64(), Name: "Laptop", Platform: "desktop", AppVersion: "0.1",
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, PairMessage(id, ch, k.pub64())))}
	d, err := r.svc.CompletePairing(ctx, id, req)
	if err != nil {
		r.t.Fatal(err)
	}
	if consent != nil {
		if d, err = r.svc.Confirm(ctx, d.ID, "owner", consent); err != nil {
			r.t.Fatal(err)
		}
	}
	return d, k
}

func (r *rig) signed(d *Device, k keyed, method, path, nonce string, body []byte, tsOffset time.Duration) error {
	ts := r.clk.Now().Add(tsOffset).UnixMilli()
	_, err := r.svc.Authenticate(context.Background(), d.ID, method, path, ts, nonce, body, ed25519.Sign(k.priv, SignMessage(method, path, ts, nonce, body)))
	return err
}

func TestPairingNeedsChallengeProofAndOwnerConfirmation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id, ch, _, _ := r.svc.StartPairing(ctx, "owner", []string{"screen.observe", "input.mouse", "input.keyboard"}, 5*time.Minute)
	k := newKey()
	good := PairRequest{Challenge: ch, PublicKey: k.pub64(), Name: "Laptop", Platform: "desktop", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, PairMessage(id, ch, k.pub64())))}
	// wrong challenge
	bad := good
	bad.Challenge = "nope"
	if _, err := r.svc.CompletePairing(ctx, id, bad); !errors.Is(err, ErrPairing) {
		t.Fatalf("wrong challenge: %v", err)
	}
	// a public key the device cannot sign for (no proof of possession)
	other := newKey()
	noProof := good
	noProof.PublicKey = other.pub64()
	if _, err := r.svc.CompletePairing(ctx, id, noProof); !errors.Is(err, ErrPairing) {
		t.Fatalf("no proof of key possession: %v", err)
	}
	// the challenge is stored only as a hash
	var h string
	r.st.R.QueryRow("select challenge_sha256 from device_pairings").Scan(&h)
	if h == ch || len(h) != 64 {
		t.Fatalf("challenge stored in clear or malformed: %q", h)
	}
	d, err := r.svc.CompletePairing(ctx, id, good)
	if err != nil || d.Trust != "pending" || len(d.Capabilities) != 0 {
		t.Fatalf("%v %+v", err, d)
	}
	// single use
	if _, err := r.svc.CompletePairing(ctx, id, good); !errors.Is(err, ErrPairing) {
		t.Fatalf("challenge reused: %v", err)
	}
	// a pending device can do nothing
	if err := r.signed(d, k, "POST", "/x", "nonce-0001", nil, 0); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("unconfirmed device authenticated: %v", err)
	}
	// the owner consents to LESS than was requested, never more
	if _, err := r.svc.Confirm(ctx, d.ID, "owner", []string{"files.write"}); !errors.Is(err, ErrCapability) {
		t.Fatalf("consent beyond the request: %v", err)
	}
	if _, err := r.svc.Confirm(ctx, d.ID, "someone-else", []string{"screen.observe"}); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("another owner confirmed: %v", err)
	}
	d, err = r.svc.Confirm(ctx, d.ID, "owner", []string{"screen.observe"})
	if err != nil || d.Trust != "trusted" || !d.Has("screen.observe") || d.Has("input.mouse") {
		t.Fatalf("%v %+v", err, d)
	}
	if err := r.signed(d, k, "POST", "/x", "nonce-0002", nil, 0); err != nil {
		t.Fatalf("trusted device refused: %v", err)
	}
}

func TestPairingExpires(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id, ch, _, _ := r.svc.StartPairing(ctx, "owner", []string{"screen.observe"}, time.Minute)
	r.clk.Add(2 * time.Minute)
	k := newKey()
	if _, err := r.svc.CompletePairing(ctx, id, PairRequest{Challenge: ch, PublicKey: k.pub64(), Name: "L", Platform: "desktop",
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, PairMessage(id, ch, k.pub64())))}); !errors.Is(err, ErrPairing) {
		t.Fatalf("expired pairing completed: %v", err)
	}
	var state string
	r.st.R.QueryRow("select state from device_pairings").Scan(&state)
	if state != "expired" {
		t.Fatalf("state %s", state)
	}
	if _, _, _, err := r.svc.StartPairing(ctx, "owner", []string{"launch.missiles"}, time.Minute); err == nil {
		t.Fatal("an unknown capability was accepted")
	}
}

func TestSignedRequestsRejectForgeryTamperingStalenessAndReplay(t *testing.T) {
	r := newRig(t)
	d, k := r.pair([]string{"screen.observe"}, []string{"screen.observe"})
	body := []byte(`{"a":1}`)
	if err := r.signed(d, k, "POST", "/api/v1/devices/self/heartbeat", "nonce-aaaa", body, 0); err != nil {
		t.Fatal(err)
	}
	// replay of the same nonce
	if err := r.signed(d, k, "POST", "/api/v1/devices/self/heartbeat", "nonce-aaaa", body, 0); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: %v", err)
	}
	// stale / future timestamps
	if err := r.signed(d, k, "POST", "/p", "nonce-bbbb", body, -2*time.Minute); !errors.Is(err, ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	if err := r.signed(d, k, "POST", "/p", "nonce-cccc", body, 2*time.Minute); !errors.Is(err, ErrStale) {
		t.Fatalf("future: %v", err)
	}
	// signed by another key
	other := newKey()
	if err := r.signed(d, other, "POST", "/p", "nonce-dddd", body, 0); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("forged: %v", err)
	}
	// a tampered body: sign one body, present another
	ts := r.clk.Now().UnixMilli()
	sig := ed25519.Sign(k.priv, SignMessage("POST", "/p", ts, "nonce-eeee", body))
	if _, err := r.svc.Authenticate(context.Background(), d.ID, "POST", "/p", ts, "nonce-eeee", []byte(`{"a":2}`), sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered body: %v", err)
	}
	// a signature for another path
	if _, err := r.svc.Authenticate(context.Background(), d.ID, "POST", "/other", ts, "nonce-eeee", body, sig); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("path swap: %v", err)
	}
	// nonces are pruned, so the table does not grow without bound
	r.clk.Add(10 * time.Minute)
	if err := r.signed(d, k, "POST", "/p", "nonce-ffff", body, 0); err != nil {
		t.Fatal(err)
	}
	var n int
	r.st.R.QueryRow("select count(*) from device_nonces").Scan(&n)
	if n != 1 {
		t.Fatalf("nonce table: %d rows", n)
	}
}

// AT-08 (device part): revocation stops the device at once and revokes what it held.
func TestRevocationStopsTheDeviceAndItsLeases(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	d, k := r.pair([]string{"browser.control"}, []string{"browser.control"})
	paused := []string{}
	l, err := r.svc.BeginLease(ctx, d, "task-1", time.Hour, func(_ context.Context, id string) (bool, error) { paused = append(paused, id); return true, nil })
	if err != nil || len(paused) != 1 {
		t.Fatalf("%v paused=%v", err, paused)
	}
	tasks, err := r.svc.Revoke(ctx, d.ID, "owner", "laptop lost")
	if err != nil || len(tasks) != 1 || tasks[0] != "task-1" {
		t.Fatalf("%v %v", err, tasks)
	}
	if err := r.signed(d, k, "POST", "/p", "nonce-zzzz", nil, 0); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("a revoked device authenticated: %v", err)
	}
	var st string
	r.st.R.QueryRow("select state from device_leases where id=?", l.ID).Scan(&st)
	if st != "revoked" {
		t.Fatalf("lease %s", st)
	}
	if active, _ := r.svc.ActiveLease(ctx, "task-1"); active {
		t.Fatal("a revoked lease still holds the task")
	}
	var n int
	r.st.R.QueryRow("select count(*) from audit_events where type='DEVICE_REVOKED'").Scan(&n)
	if n != 1 {
		t.Fatalf("audit %d", n)
	}
	// revoking again is a no-op, and a revoked device cannot be confirmed back to life
	if _, err := r.svc.Revoke(ctx, d.ID, "owner", "again"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Confirm(ctx, d.ID, "owner", []string{"browser.control"}); !errors.Is(err, ErrNotTrusted) {
		t.Fatalf("a revoked device was confirmed again: %v", err)
	}
}

// AT-11: takeover pauses automation; handback needs a fresh observation.
func TestTakeoverLeasePausesAndHandbackNeedsFreshObservation(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	d, _ := r.pair([]string{"input.mouse", "input.keyboard", "screen.observe"}, []string{"input.mouse", "input.keyboard", "screen.observe"})
	var pauses int
	pause := func(context.Context, string) (bool, error) { pauses++; return true, nil }
	l, err := r.svc.BeginLease(ctx, d, "t1", 30*time.Minute, pause)
	if err != nil || pauses != 1 {
		t.Fatalf("%v pauses=%d", err, pauses)
	}
	if active, _ := r.svc.ActiveLease(ctx, "t1"); !active {
		t.Fatal("the lease does not hold the task")
	}
	// a second takeover of the same task conflicts
	if _, err := r.svc.BeginLease(ctx, d, "t1", time.Minute, pause); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("second lease: %v", err)
	}
	// no observation, no handback
	for _, obs := range []string{"", "short", "x"} {
		if _, err := r.svc.ReleaseLease(ctx, d, l.ID, obs); !errors.Is(err, ErrNeedsObserve) {
			t.Fatalf("released without a fresh observation (%q): %v", obs, err)
		}
	}
	if active, _ := r.svc.ActiveLease(ctx, "t1"); !active {
		t.Fatal("task released without an observation")
	}
	// another device cannot release it
	d2, _ := r.pair([]string{"input.mouse", "input.keyboard"}, []string{"input.mouse", "input.keyboard"})
	if _, err := r.svc.ReleaseLease(ctx, d2, l.ID, hashHex([]byte("screen"))); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("another device released the lease: %v", err)
	}
	out, err := r.svc.ReleaseLease(ctx, d, l.ID, hashHex([]byte("screen after the owner finished")))
	if err != nil || out.State != "released" || out.Observation == "" || !out.Paused {
		t.Fatalf("%v %+v", err, out)
	}
	if active, _ := r.svc.ActiveLease(ctx, "t1"); active {
		t.Fatal("task still held after handback")
	}
	if _, err := r.svc.ReleaseLease(ctx, d, l.ID, hashHex([]byte("again"))); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("a released lease was released twice: %v", err)
	}
}

func TestLeaseNeedsTheMatchingCapabilityAndExpires(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	obs, _ := r.pair([]string{"screen.observe"}, []string{"screen.observe"})
	if _, err := r.svc.BeginLease(ctx, obs, "t1", time.Minute, nil); !errors.Is(err, ErrCapability) {
		t.Fatalf("a read-only device took over a task: %v", err)
	}
	d, _ := r.pair([]string{"browser.control"}, []string{"browser.control"})
	if _, err := r.svc.BeginLease(ctx, d, "t2", time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	r.clk.Add(2 * time.Minute)
	if active, _ := r.svc.ActiveLease(ctx, "t2"); active {
		t.Fatal("an expired lease still holds the task")
	}
	if n, _ := r.svc.ExpireLeases(ctx); n != 1 {
		t.Fatalf("expired %d", n)
	}
	// a pause that fails undoes the lease
	if _, err := r.svc.BeginLease(ctx, d, "t3", time.Minute, func(context.Context, string) (bool, error) { return false, errors.New("cannot pause") }); err == nil {
		t.Fatal("a lease began although automation could not be paused")
	}
	if active, _ := r.svc.ActiveLease(ctx, "t3"); active {
		t.Fatal("a failed pause left an active lease")
	}
}

var _ = sql.ErrNoRows
