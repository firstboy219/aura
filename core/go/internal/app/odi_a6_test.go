package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/device"
)

type devClient struct {
	e    *env
	id   string
	priv ed25519.PrivateKey
	n    int
}

func (c *devClient) call(method, path string, body string) (int, map[string]any) {
	c.n++
	ts := time.Now().UnixMilli()
	nonce := fmt.Sprintf("nonce-%d-%d", ts, c.n)
	sig := ed25519.Sign(c.priv, device.SignMessage(method, path, ts, nonce, []byte(body)))
	r, _ := http.NewRequest(method, c.e.srv.URL+path, strings.NewReader(body))
	r.Header.Set("X-Aura-Device", c.id)
	r.Header.Set("X-Aura-Timestamp", strconv.FormatInt(ts, 10))
	r.Header.Set("X-Aura-Nonce", nonce)
	r.Header.Set("X-Aura-Signature", base64.StdEncoding.EncodeToString(sig))
	return doJSON(c.e.t, r)
}

func doJSON(t *testing.T, r *http.Request) (int, map[string]any) {
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func pairOverHTTP(t *testing.T, e *env, caps string) (*devClient, string) {
	t.Helper()
	resp, m := e.req("POST", "/api/v1/devices/pairings", `{"capabilities":`+caps+`}`, e.auth())
	if resp.StatusCode != 201 {
		t.Fatalf("start pairing: %d %v", resp.StatusCode, m)
	}
	pid, ch := m["id"].(string), m["challenge"].(string)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub64 := base64.StdEncoding.EncodeToString(pub)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, device.PairMessage(pid, ch, pub64)))
	body := fmt.Sprintf(`{"challenge":%q,"public_key":%q,"name":"Laptop","platform":"desktop","app_version":"0.1","signature":%q}`, ch, pub64, sig)
	resp, m = e.req("POST", "/api/v1/devices/pairings/"+pid+"/complete", body, nil) // no owner token: the challenge and the key proof are the credentials
	if resp.StatusCode != 202 || m["trust"] != "pending" {
		t.Fatalf("complete pairing: %d %v", resp.StatusCode, m)
	}
	return &devClient{e: e, id: m["device_id"].(string), priv: priv}, pid
}

func TestDevicePairingConfirmSignedRequestsAndRevocationOverHTTP(t *testing.T) {
	e := nexusEnv(t)
	c, pid := pairOverHTTP(t, e, `["browser.control","screen.observe"]`)
	// the challenge works once
	if resp, _ := e.req("POST", "/api/v1/devices/pairings/"+pid+"/complete", `{"challenge":"x","public_key":"x","name":"x","platform":"desktop","signature":"x"}`, nil); resp.StatusCode != 403 {
		t.Fatalf("reused pairing: %d", resp.StatusCode)
	}
	// pending: nothing works, not even a correctly signed heartbeat
	if code, _ := c.call("POST", "/api/v1/devices/self/heartbeat", `{}`); code != 403 {
		t.Fatalf("an unconfirmed device was accepted: %d", code)
	}
	// owner-only endpoints refuse the world
	if resp, _ := e.req("GET", "/api/v1/devices", "", nil); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated device list: %d", resp.StatusCode)
	}
	// the owner consents to a subset, never more than requested
	if resp, _ := e.req("POST", "/api/v1/devices/"+c.id+"/confirm", `{"capabilities":["files.write"]}`, e.auth()); resp.StatusCode != 403 {
		t.Fatalf("consent beyond the request: %d", resp.StatusCode)
	}
	resp, m := e.req("POST", "/api/v1/devices/"+c.id+"/confirm", `{"capabilities":["browser.control"]}`, e.auth())
	if resp.StatusCode != 200 || m["trust"] != "trusted" {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	code, m := c.call("POST", "/api/v1/devices/self/heartbeat", `{}`)
	if code != 200 {
		t.Fatalf("%d %v", code, m)
	}
	// a replayed request (same nonce) and a request signed by someone else
	ts := time.Now().UnixMilli()
	nonce := "replayed-nonce-1"
	sig := ed25519.Sign(c.priv, device.SignMessage("POST", "/api/v1/devices/self/heartbeat", ts, nonce, []byte(`{}`)))
	send := func(sig []byte) int {
		r, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/devices/self/heartbeat", strings.NewReader(`{}`))
		r.Header.Set("X-Aura-Device", c.id)
		r.Header.Set("X-Aura-Timestamp", strconv.FormatInt(ts, 10))
		r.Header.Set("X-Aura-Nonce", nonce)
		r.Header.Set("X-Aura-Signature", base64.StdEncoding.EncodeToString(sig))
		code, _ := doJSON(t, r)
		return code
	}
	if send(sig) != 200 || send(sig) != 401 {
		t.Fatal("replay of a signed request was not refused")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if send(ed25519.Sign(other, device.SignMessage("POST", "/api/v1/devices/self/heartbeat", ts, nonce+"2", []byte(`{}`)))) != 401 {
		t.Fatal("a signature by another key was accepted")
	}
	// revoke: the device is out at once
	resp, m = e.req("POST", "/api/v1/devices/"+c.id+"/revoke", `{"reason":"laptop lost"}`, e.auth())
	if resp.StatusCode != 200 || m["revoked"] != true {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	if code, _ := c.call("POST", "/api/v1/devices/self/heartbeat", `{}`); code != 403 {
		t.Fatalf("a revoked device still authenticates: %d", code)
	}
	if auditCount(e, "DEVICE_REVOKED") != 1 || auditCount(e, "DEVICE_CONFIRMED") != 1 || auditCount(e, "DEVICE_PAIRED_PENDING") != 1 {
		t.Fatalf("audit: revoked=%d confirmed=%d pending=%d", auditCount(e, "DEVICE_REVOKED"), auditCount(e, "DEVICE_CONFIRMED"), auditCount(e, "DEVICE_PAIRED_PENDING"))
	}
}

func TestDeviceTakeoverLeaseOverHTTP(t *testing.T) {
	e := nexusEnv(t)
	c, _ := pairOverHTTP(t, e, `["browser.control"]`)
	e.req("POST", "/api/v1/devices/"+c.id+"/confirm", `{"capabilities":["browser.control"]}`, e.auth())
	resp, m := e.req("POST", "/api/v1/tasks", `{"objective":"look at the page"}`, e.auth())
	if resp.StatusCode != 201 && resp.StatusCode != 202 {
		t.Fatalf("create task: %d %v", resp.StatusCode, m)
	}
	tid := m["id"].(string)
	code, l := c.call("POST", "/api/v1/devices/self/leases", `{"task_id":"`+tid+`","ttl_minutes":10}`)
	if code != 201 {
		t.Fatalf("begin lease: %d %v", code, l)
	}
	lid := l["id"].(string)
	if code, _ := c.call("POST", "/api/v1/devices/self/leases", `{"task_id":"`+tid+`"}`); code != 409 {
		t.Fatalf("a second lease on the same task: %d", code)
	}
	if code, _ := c.call("POST", "/api/v1/devices/self/leases/"+lid+"/release", `{"observation_sha256":"short"}`); code != 422 {
		t.Fatalf("handback without a fresh observation: %d", code)
	}
	code, out := c.call("POST", "/api/v1/devices/self/leases/"+lid+"/release", `{"observation_sha256":"`+strings.Repeat("ab", 32)+`"}`)
	if code != 200 || out["lease"].(map[string]any)["state"] != "released" {
		t.Fatalf("handback: %d %v", code, out)
	}
	if auditCount(e, "DEVICE_LEASE_BEGUN") != 1 || auditCount(e, "DEVICE_LEASE_RELEASED") != 1 {
		t.Fatalf("lease audit: %d/%d", auditCount(e, "DEVICE_LEASE_BEGUN"), auditCount(e, "DEVICE_LEASE_RELEASED"))
	}
}
