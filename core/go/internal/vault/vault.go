// Package vault keeps secrets encrypted at rest (Blueprint D.1 P4, ADR-0036).
//
// Values live in the `secrets` table as AES-256-GCM ciphertext, so database
// backups carry them encrypted. The 256-bit master key is a separate 0400 file
// outside the data volume. A value is bound to its name (additional data), so
// ciphertexts cannot be swapped between names. Rotation re-encrypts every value
// under the new key in one transaction; the previous key is only needed until
// the rotation has run.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("secret not found")
	ErrNoKey    = errors.New("secret is encrypted under a key that is not loaded")
	nameRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
)

// RefPrefix marks a config secret reference that points into the vault.
const RefPrefix = "vault:"

// Vault holds the loaded keys. current encrypts; every loaded key decrypts.
type Vault struct {
	keys    map[string]cipher.AEAD
	current string
}

// KeyID is a non-secret identifier of a key (first 16 hex chars of its sha256).
func KeyID(key []byte) string {
	h := sha256.Sum256(key)
	return hex.EncodeToString(h[:8])
}

// ParseKey decodes a key file's content (base64 of 32 random bytes).
func ParseKey(text string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(k) != 32 {
		return nil, errors.New("master key must be base64 of exactly 32 bytes")
	}
	return k, nil
}

// New builds a vault from the current key and optional previous keys.
func New(current []byte, previous ...[]byte) (*Vault, error) {
	v := &Vault{keys: map[string]cipher.AEAD{}}
	for i, k := range append([][]byte{current}, previous...) {
		if k == nil {
			continue
		}
		b, err := aes.NewCipher(k)
		if err != nil {
			return nil, err
		}
		g, err := cipher.NewGCM(b)
		if err != nil {
			return nil, err
		}
		id := KeyID(k)
		v.keys[id] = g
		if i == 0 {
			v.current = id
		}
	}
	if v.current == "" {
		return nil, errors.New("no current key")
	}
	return v, nil
}

// CurrentKeyID identifies the key new values are encrypted with.
func (v *Vault) CurrentKeyID() string { return v.current }

func aad(name string) []byte { return []byte("aura-secret:" + name) }

func (v *Vault) seal(name, value string) (string, []byte, []byte, error) {
	g := v.keys[v.current]
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", nil, nil, err
	}
	return v.current, nonce, g.Seal(nil, nonce, []byte(value), aad(name)), nil
}

func (v *Vault) open(name, keyID string, nonce, ct []byte) (string, error) {
	g, ok := v.keys[keyID]
	if !ok {
		return "", fmt.Errorf("%w (key %s)", ErrNoKey, keyID)
	}
	pt, err := g.Open(nil, nonce, ct, aad(name))
	if err != nil {
		return "", fmt.Errorf("secret %s: decryption failed (wrong key or tampered value)", name)
	}
	return string(pt), nil
}

// Put stores (or replaces) a value encrypted under the current key.
func (v *Vault) Put(ctx context.Context, tx *sql.Tx, name, value string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("secret name must match %s", nameRe)
	}
	if strings.TrimSpace(value) == "" || len(value) > 16<<10 {
		return errors.New("secret value must be 1..16384 bytes")
	}
	id, nonce, ct, err := v.seal(name, value)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `insert into secrets(name, key_id, nonce, ciphertext, created_at, updated_at) values(?,?,?,?,?,?)
		on conflict(name) do update set key_id=excluded.key_id, nonce=excluded.nonce, ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`,
		name, id, nonce, ct, now, now)
	return err
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// Get decrypts one value.
func (v *Vault) Get(ctx context.Context, db querier, name string) (string, error) {
	var keyID string
	var nonce, ct []byte
	err := db.QueryRowContext(ctx, "select key_id, nonce, ciphertext from secrets where name=?", name).Scan(&keyID, &nonce, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return "", err
	}
	return v.open(name, keyID, nonce, ct)
}

// Info describes a stored secret without its value.
type Info struct {
	Name      string `json:"name"`
	KeyID     string `json:"key_id"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// List returns names and key ids, never values.
func List(ctx context.Context, db querier) ([]Info, error) {
	rows, err := db.QueryContext(ctx, "select name, key_id, created_at, updated_at from secrets order by name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		var i Info
		if err := rows.Scan(&i.Name, &i.KeyID, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Rotate re-encrypts every value not yet under the current key, in the caller's
// transaction (all or nothing). It returns how many values changed.
func (v *Vault) Rotate(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, "select name, key_id, nonce, ciphertext from secrets where key_id <> ?", v.current)
	if err != nil {
		return 0, err
	}
	type row struct {
		name, keyID string
		nonce, ct   []byte
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.keyID, &r.nonce, &r.ct); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, r)
	}
	rows.Close()
	for _, r := range todo {
		pt, err := v.open(r.name, r.keyID, r.nonce, r.ct)
		if err != nil {
			return 0, err
		}
		if err := v.Put(ctx, tx, r.name, pt); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// GenerateKeyFile writes a new random master key (base64) to path with mode
// 0400. It refuses to overwrite an existing file.
func GenerateKeyFile(path string) (string, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(k) + "\n"); err != nil {
		return "", err
	}
	return KeyID(k), f.Sync()
}
