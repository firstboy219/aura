// Package sandbox runs untrusted code in WebAssembly (Blueprint 8A.1 L2, D.1 P4,
// ADR-0038). It uses wazero (pure Go, no cgo) with only WASI preview 1 host
// functions: a module that imports anything else fails to instantiate. WASI
// preview 1 has no way to open network connections, and no directory is
// mounted, so code sees no host files. Memory, wall time and output size are
// bounded per run.
//
// L2 tools are Python: CPython 3.12 compiled to WASI (stdlib embedded) runs the
// tool source with the JSON input on stdin and must print one JSON value.
package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// PythonWASMSHA256 pins the CPython WASI build (vmware-labs
// webassembly-language-runtimes, python/3.12.0+20231211-040d5a6).
const PythonWASMSHA256 = "e5dc5a398b07b54ea8fdb503bf68fb583d533f10ec3f930963e02b9505f7a763"

var (
	ErrTimeout   = errors.New("sandbox: time limit exceeded")
	ErrOutput    = errors.New("sandbox: output limit exceeded")
	ErrExit      = errors.New("sandbox: program failed")
	ErrForbidden = errors.New("sandbox: module imports an undeclared host function")
)

// Limits bound one run.
type Limits struct {
	Timeout     time.Duration // default 10 s
	MemoryPages uint32        // 64 KiB pages; default 2048 (128 MiB)
	MaxOutput   int           // bytes of stdout; default 256 KiB
}

func (l Limits) withDefaults() Limits {
	if l.Timeout <= 0 {
		l.Timeout = 10 * time.Second
	}
	if l.MemoryPages == 0 {
		l.MemoryPages = 2048
	}
	if l.MaxOutput <= 0 {
		l.MaxOutput = 256 << 10
	}
	return l
}

// Runtime compiles the Python module once and runs it many times.
type Runtime struct {
	mu     sync.Mutex
	cache  wazero.CompilationCache
	python []byte
	limits Limits
	rt     wazero.Runtime
	mod    wazero.CompiledModule
}

// NewPython loads and verifies the CPython WASI module. cacheDir (optional)
// keeps compiled machine code across restarts.
func NewPython(ctx context.Context, wasmPath, cacheDir string, limits Limits) (*Runtime, error) {
	b, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("sandbox: python module: %w", err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != PythonWASMSHA256 {
		return nil, errors.New("sandbox: python module does not match the pinned sha256")
	}
	r := &Runtime{python: b, limits: limits.withDefaults()}
	if cacheDir != "" {
		if c, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			r.cache = c
		}
	}
	if err := r.init(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Runtime) init(ctx context.Context) error {
	cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(r.limits.MemoryPages).WithCloseOnContextDone(true)
	if r.cache != nil {
		cfg = cfg.WithCompilationCache(r.cache)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return err
	}
	mod, err := rt.CompileModule(ctx, r.python)
	if err != nil {
		rt.Close(ctx)
		return err
	}
	if err := CheckImports(mod); err != nil {
		rt.Close(ctx)
		return err
	}
	r.rt, r.mod = rt, mod
	return nil
}

// CheckImports refuses a module that imports anything but WASI preview 1.
func CheckImports(mod wazero.CompiledModule) error {
	for _, f := range mod.ImportedFunctions() {
		if m, _, _ := f.Import(); m != wasi_snapshot_preview1.ModuleName {
			return fmt.Errorf("%w: %s", ErrForbidden, m)
		}
	}
	for _, m := range mod.ImportedMemories() {
		if mn, _, _ := m.Import(); mn != wasi_snapshot_preview1.ModuleName {
			return fmt.Errorf("%w: memory from %s", ErrForbidden, mn)
		}
	}
	return nil
}

// Result of one run.
type Result struct {
	Stdout   []byte
	Stderr   string
	Duration time.Duration
}

type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		c.over = true
		return 0, ErrOutput
	}
	return c.buf.Write(p)
}

// RunPython runs Python source with stdin; argv[1:] are passed to the script.
// Nothing is mounted: no host file is visible, and there is no network.
func (r *Runtime) RunPython(ctx context.Context, source string, stdin []byte) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.limits.Timeout)
	defer cancel()
	out := &capped{max: r.limits.MaxOutput}
	errBuf := &capped{max: 64 << 10}
	mc := wazero.NewModuleConfig().
		WithName(""). // anonymous: many runs may coexist
		WithArgs("python", "-I", "-S", "-c", source).
		WithStdin(bytes.NewReader(stdin)).WithStdout(out).WithStderr(errBuf).
		WithEnv("PYTHONDONTWRITEBYTECODE", "1").WithEnv("PYTHONHASHSEED", "0").
		WithSysWalltime().WithSysNanotime().WithRandSource(randReader{})
	start := time.Now()
	r.mu.Lock()
	rt, mod := r.rt, r.mod
	r.mu.Unlock()
	m, err := rt.InstantiateModule(ctx, mod, mc)
	if m != nil {
		m.Close(context.Background())
	}
	res := &Result{Stdout: out.buf.Bytes(), Stderr: errBuf.buf.String(), Duration: time.Since(start)}
	if ctx.Err() == context.DeadlineExceeded {
		return res, ErrTimeout
	}
	if out.over {
		return res, ErrOutput
	}
	if err != nil {
		var exit *sys.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 0 {
			return res, nil
		}
		return res, fmt.Errorf("%w: %s", ErrExit, lastLine(res.Stderr, err))
	}
	return res, nil
}

func lastLine(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if l := strings.TrimSpace(lines[len(lines)-1]); l != "" {
		if len(l) > 300 {
			l = l[:300]
		}
		return l
	}
	return err.Error()
}

// Close releases the runtime.
func (r *Runtime) Close(ctx context.Context) error {
	if r.rt != nil {
		return r.rt.Close(ctx)
	}
	return nil
}

// CompileUntrusted compiles an arbitrary module and checks its imports
// (used to prove undeclared host calls fail).
func (r *Runtime) CompileUntrusted(ctx context.Context, wasm []byte) error {
	mod, err := r.rt.CompileModule(ctx, wasm)
	if err != nil {
		return err
	}
	defer mod.Close(ctx)
	return CheckImports(mod)
}

// InstantiateUntrusted tries to instantiate an arbitrary module with only WASI
// available; an undeclared import fails here even without CheckImports.
func (r *Runtime) InstantiateUntrusted(ctx context.Context, wasm []byte) (api.Module, error) {
	return r.rt.InstantiateWithConfig(ctx, wasm, wazero.NewModuleConfig().WithName(""))
}

type randReader struct{}

func (randReader) Read(p []byte) (int, error) { return cryptoRead(p) }

var _ io.Reader = randReader{}
