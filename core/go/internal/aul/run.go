package aul

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"aura.local/core/internal/aul/ir"
	"aura.local/core/internal/sandbox"
)

// Host answers the VM's tool calls; the Core implements it with its grant checks.
type Host interface {
	Call(ctx context.Context, tool string, args map[string]any) (any, error)
}

// Limits for one run.
type Limits struct {
	Timeout     time.Duration
	MaxOps      int
	MaxCalls    int
	MemoryPages uint32 // 64 KiB pages
	MaxMessage  int    // bytes per VM message
}

func (l Limits) withDefaults() Limits {
	if l.Timeout <= 0 {
		l.Timeout = 2 * time.Minute
	}
	if l.MaxOps <= 0 {
		l.MaxOps = 200000
	}
	if l.MaxCalls <= 0 {
		l.MaxCalls = 50
	}
	if l.MemoryPages == 0 {
		l.MemoryPages = 2048 // 128 MiB
	}
	if l.MaxMessage <= 0 {
		l.MaxMessage = 2 << 20
	}
	return l
}

// Errors of a run.
var (
	ErrVM       = errors.New("aul_vm_error")
	ErrTimeout  = errors.New("aul_timeout")
	ErrTooMany  = errors.New("aul_call_limit")
	ErrWorkflow = errors.New("aul_workflow_failed")
)

// Runtime holds the compiled AUL VM module.
type Runtime struct {
	rt     wazero.Runtime
	mod    wazero.CompiledModule
	SHA256 string
	limits Limits
	mu     sync.Mutex
}

// NewRuntime compiles the VM module. It must import only WASI preview 1.
func NewRuntime(ctx context.Context, wasmPath, cacheDir string, limits Limits) (*Runtime, error) {
	b, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, err
	}
	limits = limits.withDefaults()
	cfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(limits.MemoryPages).WithCloseOnContextDone(true)
	if cacheDir != "" {
		if c, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
			cfg = cfg.WithCompilationCache(c)
		}
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	mod, err := rt.CompileModule(ctx, b)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	if err := sandbox.CheckImports(mod); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return &Runtime{rt: rt, mod: mod, SHA256: sha256Hex(b), limits: limits}, nil
}

// Close releases the runtime.
func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }

// Result of a run.
type Result struct {
	Output any           `json:"output"`
	Calls  int           `json:"calls"`
	Took   time.Duration `json:"took_ns"`
}

// Run executes a checked program in a fresh VM instance. Every tool call goes
// to host; the run ends on completion, failure, the call or op limit, or the
// timeout (the module is closed with the context).
func (r *Runtime) Run(ctx context.Context, p *ir.Program, inputs map[string]any, host Host) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.limits.Timeout)
	defer cancel()
	toVM, vmIn := io.Pipe()
	fromVM, vmOut := io.Pipe()
	mc := wazero.NewModuleConfig().WithName("").WithArgs("aulvm").WithStdin(toVM).WithStdout(vmOut).WithStderr(io.Discard)
	done := make(chan error, 1)
	go func() {
		m, err := r.rt.InstantiateModule(ctx, r.mod, mc)
		if m != nil {
			m.Close(context.Background())
		}
		vmOut.Close()
		toVM.Close()
		done <- err
	}()
	start := time.Now()
	enc := json.NewEncoder(vmIn)
	defer vmIn.Close()
	if err := enc.Encode(ir.Message{Program: p, Inputs: inputs, MaxOps: r.limits.MaxOps}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVM, err)
	}
	rd := bufio.NewReaderSize(fromVM, 64<<10)
	res := &Result{}
	for {
		line, err := readLine(rd, r.limits.MaxMessage)
		if err != nil {
			cancel()
			<-done
			if ctx.Err() == context.DeadlineExceeded {
				return res, ErrTimeout
			}
			return res, fmt.Errorf("%w: %v", ErrVM, err)
		}
		var m ir.Message
		if err := json.Unmarshal(line, &m); err != nil {
			cancel()
			<-done
			return res, fmt.Errorf("%w: malformed VM message", ErrVM)
		}
		if m.Done {
			cancel()
			<-done
			res.Took = time.Since(start)
			if m.Error != "" {
				return res, fmt.Errorf("%w: %s", ErrWorkflow, m.Error)
			}
			res.Output = m.Output
			return res, nil
		}
		res.Calls++
		if res.Calls > r.limits.MaxCalls {
			cancel()
			<-done
			return res, fmt.Errorf("%w: more than %d tool calls", ErrTooMany, r.limits.MaxCalls)
		}
		reply := ir.Message{OK: true}
		out, err := host.Call(ctx, m.Call, m.Args)
		if err != nil {
			reply = ir.Message{Error: err.Error()}
		} else {
			reply.Result = out
		}
		if err := enc.Encode(reply); err != nil {
			cancel()
			<-done
			return res, fmt.Errorf("%w: %v", ErrVM, err)
		}
	}
}

func readLine(rd *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	for {
		chunk, isPrefix, err := rd.ReadLine()
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(out) > max {
			return nil, errors.New("VM message too large")
		}
		if !isPrefix {
			return out, nil
		}
	}
}
