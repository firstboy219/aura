package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func image(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range map[string]string{
		"usr/local/bin/aura":                         "binary v1",
		"usr/lib/site-packages/aura_worker/main.py":  "print('worker')",
		"usr/lib/site-packages/aura_worker/agent.py": "def plan(): pass",
	} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "usr/lib/site-packages/aura_worker/__pycache__"), 0o755)
	os.WriteFile(filepath.Join(root, "usr/lib/site-packages/aura_worker/__pycache__/x.pyc"), []byte("cache"), 0o644)
	return root
}

func signed(t *testing.T, root string) ([]byte, string) {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "release.key")
	pubText, err := Keygen(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(keyFile)
	priv, err := ParsePrivateKey(string(b))
	if err != nil {
		t.Fatal(err)
	}
	files, err := Collect(root, []string{"/usr/local/bin/aura", "/usr/lib/site-packages/aura_worker"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Sign(priv, "0.2.0-p4", files, []string{"/usr/lib/site-packages/aura_worker"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	return raw, pubText
}

func TestSignedReleaseVerifies(t *testing.T) {
	root := image(t)
	raw, pubText := signed(t, root)
	pub, _ := ParsePublicKey(pubText)
	m, err := Verify(raw, pub, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Body.Files) != 4 { // bytecode in __pycache__ is signed too
		t.Fatalf("files %v", m.Body.Files)
	}
}

func TestTamperingIsRejected(t *testing.T) {
	cases := map[string]func(root string, raw []byte) []byte{
		"changed binary": func(root string, raw []byte) []byte {
			os.WriteFile(filepath.Join(root, "usr/local/bin/aura"), []byte("binary v2"), 0o755)
			return raw
		},
		"added module": func(root string, raw []byte) []byte {
			os.WriteFile(filepath.Join(root, "usr/lib/site-packages/aura_worker/evil.py"), []byte("import os"), 0o644)
			return raw
		},
		"removed file": func(root string, raw []byte) []byte {
			os.Remove(filepath.Join(root, "usr/lib/site-packages/aura_worker/agent.py"))
			return raw
		},
		"edited manifest": func(root string, raw []byte) []byte {
			return []byte(strings.Replace(string(raw), "0.2.0-p4", "0.2.1-p4", 1))
		},
	}
	for name, tamper := range cases {
		root := image(t)
		raw, pubText := signed(t, root)
		pub, _ := ParsePublicKey(pubText)
		if _, err := Verify(tamper(root, raw), pub, root); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// A manifest signed with another key is refused.
	root := image(t)
	raw, _ := signed(t, root)
	_, otherPub := signed(t, root)
	pub, _ := ParsePublicKey(otherPub)
	if _, err := Verify(raw, pub, root); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("foreign key: %v", err)
	}
}

func TestKeyFilesAndParsing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	if _, err := Keygen(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o400 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	if _, err := Keygen(p); err == nil {
		t.Fatal("overwrote an existing key")
	}
	if _, err := ParsePublicKey("bm9wZQ=="); err == nil {
		t.Fatal("short public key accepted")
	}
}
