package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// PythonWASM locates the pinned CPython WASI module for tests
// (deploy/fetch-python-wasm.sh puts it in .cache/).
func PythonWASM(t testing.TB) string {
	t.Helper()
	if p := os.Getenv("AURA_PYTHON_WASM"); p != "" {
		return p
	}
	p, _ := filepath.Abs("../../../../.cache/python-3.12.0.wasm")
	if _, err := os.Stat(p); err != nil {
		t.Skip("python WASM module not present; run deploy/fetch-python-wasm.sh")
	}
	return p
}

var (
	shared     *Runtime
	sharedOnce sync.Once
	sharedErr  error
)

func runtime(t *testing.T) *Runtime {
	t.Helper()
	path := PythonWASM(t)
	sharedOnce.Do(func() {
		shared, sharedErr = NewPython(context.Background(), path, filepath.Join(os.TempDir(), "aura-wasm-cache"),
			Limits{Timeout: 20 * time.Second, MemoryPages: 2048, MaxOutput: 64 << 10})
	})
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return shared
}

func TestPythonRunsWithJSONIO(t *testing.T) {
	r := runtime(t)
	res, err := r.RunPython(context.Background(), "import json,sys; d=json.load(sys.stdin); print(json.dumps({'sum': sum(d['xs'])}))", []byte(`{"xs":[1,2,3.5]}`))
	if err != nil || strings.TrimSpace(string(res.Stdout)) != `{"sum": 6.5}` {
		t.Fatalf("%v %q stderr=%s", err, res.Stdout, res.Stderr)
	}
	t.Logf("run took %v", res.Duration)
}

// No host file is visible and there is no network.
func TestNoHostFilesNoNetwork(t *testing.T) {
	r := runtime(t)
	probe := `
import json, os, socket
out = {}
for p in ["/etc/passwd", "/var/lib/aura/aura.db", "/run/aura-secrets/openrouter_key", "/proc/self/environ"]:
    try:
        open(p).read(1); out[p] = "READ"
    except Exception as e:
        out[p] = type(e).__name__
try:
    out["listdir /"] = os.listdir("/")
except Exception as e:
    out["listdir /"] = type(e).__name__
try:
    socket.create_connection(("1.1.1.1", 443), timeout=2); out["net"] = "CONNECTED"
except Exception as e:
    out["net"] = type(e).__name__
out["env"] = sorted(os.environ)
print(json.dumps(out))
`
	res, err := r.RunPython(context.Background(), probe, nil)
	if err != nil {
		t.Fatalf("%v stderr=%s", err, res.Stderr)
	}
	s := string(res.Stdout)
	if strings.Contains(s, `"READ"`) || strings.Contains(s, "CONNECTED") || strings.Contains(s, "OPENROUTER") || strings.Contains(s, "AURA") {
		t.Fatalf("sandbox leaked: %s", s)
	}
	t.Log(s)
}

func TestLimits(t *testing.T) {
	r := runtime(t)
	fast, _ := NewPython(context.Background(), PythonWASM(t), filepath.Join(os.TempDir(), "aura-wasm-cache"),
		Limits{Timeout: 3 * time.Second, MemoryPages: 1024, MaxOutput: 1024})
	defer fast.Close(context.Background())
	start := time.Now()
	if _, err := fast.RunPython(context.Background(), "while True: pass", nil); !errors.Is(err, ErrTimeout) {
		t.Fatalf("infinite loop: %v", err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("timeout not enforced promptly")
	}
	if _, err := fast.RunPython(context.Background(), "x = bytearray(200*1024*1024); print(len(x))", nil); err == nil {
		t.Fatal("memory limit not enforced")
	}
	if _, err := fast.RunPython(context.Background(), "print('x'*100000)", nil); !errors.Is(err, ErrOutput) {
		t.Fatalf("output limit: %v", err)
	}
	if _, err := r.RunPython(context.Background(), "raise SystemExit(3)", nil); !errors.Is(err, ErrExit) {
		t.Fatalf("exit code: %v", err)
	}
}

// A module importing anything but WASI fails (Blueprint D.1 P4: undeclared
// WASM host calls fail).
func TestUndeclaredHostCallFails(t *testing.T) {
	r := runtime(t)
	evil := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: () -> ()
		0x02, 0x09, 0x01, 0x03, 'e', 'n', 'v', 0x01, 'f', 0x00, 0x00} // import env.f
	if err := r.CompileUntrusted(context.Background(), evil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("static import check: %v", err)
	}
	if _, err := r.InstantiateUntrusted(context.Background(), evil); err == nil {
		t.Fatal("module with an undeclared host import was instantiated")
	}
}

func TestPinnedModule(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "python.wasm")
	os.WriteFile(bad, []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	if _, err := NewPython(context.Background(), bad, "", Limits{}); err == nil {
		t.Fatal("unpinned module accepted")
	}
}
