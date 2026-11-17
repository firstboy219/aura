package vault

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aura.local/core/internal/store"
	_ "modernc.org/sqlite"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func open(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "aura.db"), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir
}

func put(t *testing.T, st *store.Store, v *Vault, name, value string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Write(ctx, func(tx *sql.Tx) error { return v.Put(ctx, tx, name, value) }); err != nil {
		t.Fatal(err)
	}
}

func TestEncryptedAtRestAndBoundToName(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()
	v, _ := New(key(1))
	put(t, st, v, "openrouter", "sk-or-v1-plaintext-value")
	put(t, st, v, "brave", "brave-plaintext-value")
	var ct []byte
	st.R.QueryRow("select ciphertext from secrets where name='openrouter'").Scan(&ct)
	if bytes.Contains(ct, []byte("plaintext")) {
		t.Fatal("value stored in clear")
	}
	if got, err := v.Get(ctx, st.R, "openrouter"); err != nil || got != "sk-or-v1-plaintext-value" {
		t.Fatalf("%q %v", got, err)
	}
	// Wrong key: refused, not garbage.
	other, _ := New(key(2))
	if _, err := other.Get(ctx, st.R, "openrouter"); err == nil {
		t.Fatal("decrypted with the wrong key")
	}
	// A ciphertext moved to another name does not decrypt (name is authenticated).
	st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update secrets set nonce=(select nonce from secrets where name='openrouter'), ciphertext=(select ciphertext from secrets where name='openrouter') where name='brave'")
		return err
	})
	if _, err := v.Get(ctx, st.R, "brave"); err == nil {
		t.Fatal("swapped ciphertext accepted")
	}
	// Tampering is detected.
	st.Write(ctx, func(tx *sql.Tx) error {
		// the last byte really changes (setting it to 0x00 was a no-op 1 time in 256: the test was flaky)
		_, err := tx.Exec("update secrets set ciphertext = substr(ciphertext,1,length(ciphertext)-1) || (case when substr(ciphertext,-1) = x'00' then x'01' else x'00' end) where name='openrouter'")
		return err
	})
	if _, err := v.Get(ctx, st.R, "openrouter"); err == nil || !strings.Contains(err.Error(), "decryption failed") {
		t.Fatalf("tampered value: %v", err)
	}
	if _, err := v.Get(ctx, st.R, "missing"); err == nil {
		t.Fatal("missing secret returned")
	}
}

// Key rotation: every value moves to the new key in one transaction; after it
// the old key is no longer needed.
func TestKeyRotation(t *testing.T) {
	st, _ := open(t)
	ctx := context.Background()
	old, _ := New(key(1))
	put(t, st, old, "a", "value-a")
	put(t, st, old, "b", "value-b")
	both, _ := New(key(2), key(1))
	var n int
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = both.Rotate(ctx, tx)
		return err
	}); err != nil || n != 2 {
		t.Fatalf("rotated %d: %v", n, err)
	}
	onlyNew, _ := New(key(2))
	for name, want := range map[string]string{"a": "value-a", "b": "value-b"} {
		if got, err := onlyNew.Get(ctx, st.R, name); err != nil || got != want {
			t.Fatalf("%s after rotation: %q %v", name, got, err)
		}
	}
	if _, err := old.Get(ctx, st.R, "a"); err == nil {
		t.Fatal("old key still decrypts after rotation")
	}
	infos, _ := List(ctx, st.R)
	for _, i := range infos {
		if i.KeyID != KeyID(key(2)) {
			t.Fatalf("%s still under %s", i.Name, i.KeyID)
		}
	}
	// Rotating without the previous key fails and changes nothing.
	put(t, st, old, "c", "value-c")
	err := st.Write(ctx, func(tx *sql.Tx) error { _, err := onlyNew.Rotate(ctx, tx); return err })
	if err == nil {
		t.Fatal("rotation without the previous key succeeded")
	}
}

// Backups carry the secrets encrypted; a restored backup decrypts with the key.
func TestBackupRestoreKeepsSecretsEncrypted(t *testing.T) {
	st, dir := open(t)
	ctx := context.Background()
	v, _ := New(key(7))
	put(t, st, v, "openrouter", "sk-or-v1-backup-test-value")
	path, err := st.Backup(ctx, "vault-test")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("backup-test-value")) {
		t.Fatal("secret in clear inside the backup file")
	}
	restored := filepath.Join(dir, "restored.db")
	if err := os.WriteFile(restored, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(ctx, restored, filepath.Join(dir, "backups2"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got, err := v.Get(ctx, st2.R, "openrouter"); err != nil || got != "sk-or-v1-backup-test-value" {
		t.Fatalf("restored: %q %v", got, err)
	}
	if _, err := (func() (string, error) { w, _ := New(key(8)); return w.Get(ctx, st2.R, "openrouter") })(); err == nil {
		t.Fatal("restored backup decrypts without the master key")
	}
}

func TestKeyFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "master.key")
	id, err := GenerateKeyFile(p)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	b, _ := os.ReadFile(p)
	k, err := ParseKey(string(b))
	if err != nil || fi.Mode().Perm() != 0o400 || KeyID(k) != id {
		t.Fatalf("key file: %v mode %o", err, fi.Mode().Perm())
	}
	if _, err := GenerateKeyFile(p); err == nil {
		t.Fatal("existing key file overwritten")
	}
	if _, err := ParseKey("c2hvcnQ="); err == nil {
		t.Fatal("short key accepted")
	}
}
