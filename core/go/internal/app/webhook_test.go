package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/store"
	"aura.local/core/internal/vault"
)

const hookSecret = "0123456789abcdef0123456789abcdef-test"

func signed(body string, at time.Time, secret string) map[string]string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return map[string]string{"X-Aura-Timestamp": ts, "X-Aura-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
}

// Blueprint §8.11 authenticated inbound webhooks (ADR-0034).
func TestSignedWebhookRaisesEventOnce(t *testing.T) {
	e := newEnvWith(t, 100, func(cfg *config.Config, dir string) {
		f := filepath.Join(dir, "hook_secret")
		os.WriteFile(f, []byte(hookSecret), 0o400)
		cfg.Webhooks = []config.Webhook{{Name: "ci", SecretFile: f, EventKey: "build-42"}}
	})
	body := `{"status":"green"}`
	now := time.Now()
	countEvents := func() int {
		var n int
		e.a.Store.R.QueryRow("select count(*) from external_events where key='build-42'").Scan(&n)
		return n
	}

	resp, m := e.req("POST", "/api/v1/webhooks/ci", body, signed(body, now, hookSecret))
	if resp.StatusCode != 202 || m["duplicate"] != false || countEvents() != 1 {
		t.Fatalf("valid webhook: %d %v events=%d", resp.StatusCode, m, countEvents())
	}
	// The same signed request again (a replay) is a duplicate, not a second event.
	if resp, m := e.req("POST", "/api/v1/webhooks/ci", body, signed(body, now, hookSecret)); resp.StatusCode != 200 || m["duplicate"] != true || countEvents() != 1 {
		t.Fatalf("replay: %d %v events=%d", resp.StatusCode, m, countEvents())
	}
	for name, hdr := range map[string]map[string]string{
		"no signature":  nil,
		"wrong secret":  signed(body, now, strings.Repeat("x", 40)),
		"stale":         signed(body, now.Add(-10*time.Minute), hookSecret),
		"future":        signed(body, now.Add(10*time.Minute), hookSecret),
		"bearer only":   e.auth(),
		"tampered body": signed(`{"status":"red"}`, now, hookSecret),
	} {
		if resp, _ := e.req("POST", "/api/v1/webhooks/ci", body, hdr); resp.StatusCode != 401 {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
	}
	if resp, _ := e.req("POST", "/api/v1/webhooks/other", body, signed(body, now, hookSecret)); resp.StatusCode != 404 {
		t.Fatalf("unknown webhook: %d", resp.StatusCode)
	}
	big := `{"x":"` + strings.Repeat("a", 17<<10) + `"}`
	if resp, _ := e.req("POST", "/api/v1/webhooks/ci", big, signed(big, now, hookSecret)); resp.StatusCode != 413 {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}
	if resp, _ := e.req("POST", "/api/v1/webhooks/ci", "not json", signed("not json", now, hookSecret)); resp.StatusCode != 400 {
		t.Fatalf("non-JSON body: %d", resp.StatusCode)
	}
	if countEvents() != 1 {
		t.Fatalf("rejected requests raised events: %d", countEvents())
	}
	var failed int
	e.a.Store.R.QueryRow("select count(*) from audit_events where type='WEBHOOK_AUTH_FAILED'").Scan(&failed)
	if failed != 6 {
		t.Fatalf("auth failures audited: %d", failed)
	}
}

func TestWebhookConfigValidation(t *testing.T) {
	for _, y := range []string{
		"webhooks: [{name: CI, secret_file: /x, event_key: k}]",
		"webhooks: [{name: ci, secret_file: '', event_key: k}]",
		"webhooks: [{name: ci, secret_file: /x, event_key: 'bad key'}]",
		"webhooks: [{name: ci, secret_file: /x, event_key: k}, {name: ci, secret_file: /y, event_key: k}]",
	} {
		if _, err := config.Parse([]byte("config_version: 1\n"+y+"\n"), func(string) string { return "" }); err == nil {
			t.Fatalf("accepted %q", y)
		}
	}
}

// ADR-0036: a configured secret may be "vault:<name>"; it is decrypted at start.
func TestVaultSecretReference(t *testing.T) {
	var keyFile string
	e := newEnvWith(t, 100, func(cfg *config.Config, dir string) {
		keyFile = filepath.Join(dir, "master.key")
		if _, err := vault.GenerateKeyFile(keyFile); err != nil {
			t.Fatal(err)
		}
		cfg.Secrets.MasterKeyFile = keyFile
		v, err := OpenVault(cfg)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(context.Background(), cfg.Database.Path, cfg.Database.BackupDir)
		if err != nil {
			t.Fatal(err)
		}
		st.Write(context.Background(), func(tx *sql.Tx) error { return v.Put(context.Background(), tx, "hook-ci", hookSecret) })
		st.Close()
		cfg.Webhooks = []config.Webhook{{Name: "ci", SecretFile: "vault:hook-ci", EventKey: "build-7"}}
	})
	if string(e.a.API.Webhooks["ci"].Secret) != hookSecret {
		t.Fatal("vault reference not resolved")
	}
	body := `{"ok":true}`
	if resp, _ := e.req("POST", "/api/v1/webhooks/ci", body, signed(body, time.Now(), hookSecret)); resp.StatusCode != 202 {
		t.Fatalf("webhook with a vault secret: %d", resp.StatusCode)
	}
}

func TestVaultReferenceWithoutMasterKeyFails(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := config.Parse([]byte("config_version: 1\n"), func(string) string { return "" })
	cfg.Server.TokenFile = filepath.Join(dir, "api_token")
	cfg.Database.Path = filepath.Join(dir, "aura.db")
	cfg.Database.BackupDir = filepath.Join(dir, "backups")
	cfg.Workspace.Root = filepath.Join(dir, "ws")
	cfg.Worker.ScratchRoot = filepath.Join(dir, "scratch")
	cfg.Webhooks = []config.Webhook{{Name: "ci", SecretFile: "vault:hook-ci", EventKey: "k"}}
	if _, err := Build(context.Background(), cfg, Options{Provider: &gatewaytest.Fake{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}); err == nil {
		t.Fatal("started with a vault reference and no master key")
	}
}
