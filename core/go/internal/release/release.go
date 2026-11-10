// Package release signs and verifies runtime artifacts (Blueprint D.1 P4,
// ADR-0037). A release manifest lists the sha256 of every runtime file (the
// aura binary, the Python worker package) and carries an Ed25519 signature
// made with the owner's release key, which never enters the repository or
// the image. The Core and the worker host verify the manifest against a public
// key configured outside the image before they start.
package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File is one signed runtime file.
type File struct {
	Path   string `json:"path"` // absolute path inside the runtime image
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Body is the signed part of a manifest.
type Body struct {
	Format    int    `json:"format"` // 1
	Version   string `json:"version"`
	CreatedAt string `json:"created_at"`
	KeyID     string `json:"key_id"`
	Files     []File `json:"files"`
	// Dirs must contain exactly the signed files: an added module fails too.
	Dirs []string `json:"dirs"`
}

// Manifest is a body and its signature (base64 Ed25519 over the body's JSON).
type Manifest struct {
	Body      Body   `json:"body"`
	Signature string `json:"signature"`
}

// KeyID identifies a public key (first 16 hex chars of its sha256).
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("release public key must be base64 of 32 bytes")
	}
	return ed25519.PublicKey(b), nil
}

// ParsePrivateKey decodes a base64 Ed25519 seed (32 bytes).
func ParsePrivateKey(text string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("release private key must be base64 of a 32-byte seed")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

// Keygen writes a new private key (seed, 0400) and returns the public key.
func Keygen(privPath string) (string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(privPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), f.Sync()
}

func hashFile(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return File{}, err
	}
	return File{Path: path, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// Collect hashes the given files and every regular file under the given
// directories (paths are rewritten from root-relative to image-absolute by
// the caller's root: path inside root "r/usr/x" is recorded as "/usr/x").
func Collect(root string, paths []string) ([]File, error) {
	if root == "" {
		root = "/"
	}
	var out []File
	for _, p := range paths {
		full := filepath.Join(root, p)
		err := filepath.WalkDir(full, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil // __pycache__ included: Python would load planted bytecode
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s: not a regular file", fp)
			}
			f, err := hashFile(fp)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, fp)
			f.Path = "/" + filepath.ToSlash(rel)
			out = append(out, f)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Sign builds a manifest for files; dirs are the signed directories.
func Sign(priv ed25519.PrivateKey, version string, files []File, dirs []string) (*Manifest, error) {
	if len(files) == 0 {
		return nil, errors.New("nothing to sign")
	}
	body := Body{Format: 1, Version: version, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		KeyID: KeyID(priv.Public().(ed25519.PublicKey)), Files: files, Dirs: dirs}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return &Manifest{Body: body, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))}, nil
}

// Verify checks the signature and then every listed file under root ("" =
// the real filesystem). Any missing, changed or unsigned-key file fails.
func Verify(raw []byte, pub ed25519.PublicKey, root string) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	b, err := json.Marshal(m.Body)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(pub, b, sig) {
		return nil, errors.New("release manifest: signature does not verify with the configured public key")
	}
	if m.Body.Format != 1 || len(m.Body.Files) == 0 {
		return nil, errors.New("release manifest: unsupported format or empty")
	}
	signed := map[string]bool{}
	for _, f := range m.Body.Files {
		signed[f.Path] = true
		got, err := hashFile(filepath.Join(root, f.Path))
		if err != nil {
			return nil, fmt.Errorf("release file %s: %w", f.Path, err)
		}
		if got.SHA256 != f.SHA256 || got.Size != f.Size {
			return nil, fmt.Errorf("release file %s: content differs from the signed manifest", f.Path)
		}
	}
	for _, d := range m.Body.Dirs {
		present, err := Collect(root, []string{d})
		if err != nil {
			return nil, fmt.Errorf("release dir %s: %w", d, err)
		}
		for _, f := range present {
			if !signed[f.Path] {
				return nil, fmt.Errorf("release dir %s: unsigned file %s", d, f.Path)
			}
		}
	}
	return &m, nil
}
