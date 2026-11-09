// Package worker supervises the Python worker process (Blueprint §9, E.2 item 2):
// one worker, spawned on demand, kept warm for an idle timeout, restarted after
// a crash, stopped with SIGTERM then SIGKILL. Its environment is built from an
// allowlist so no secret can reach it (ADR-0007).
package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/obs"
	"aura.local/core/internal/rpc"
)

var (
	ErrCrashed  = errors.New("worker_crashed")
	ErrStopped  = errors.New("worker_stopped")
	ErrProtocol = errors.New("worker_protocol_error")
)

// Handler serves worker→Core requests made while a Core→worker call is active.
type Handler func(ctx context.Context, req *rpc.Envelope) (any, *rpc.Error)

// Options configure the supervisor.
type Options struct {
	Command     []string
	ScratchRoot string
	IdleTimeout time.Duration
	Grace       time.Duration
	Log         *slog.Logger
	// ExtraEnv is appended to the allowlisted environment (tests only; never secrets).
	ExtraEnv []string
}

// allowedEnv are the only variables inherited from the Core environment.
var allowedEnv = []string{"PATH", "LANG", "LC_ALL", "TZ", "PYTHONPATH", "VIRTUAL_ENV"}

type proc struct {
	cmd     *exec.Cmd
	w       *rpc.Writer
	gen     int64
	mu      sync.Mutex
	pending map[string]chan *rpc.Envelope
	handler Handler
	hctx    context.Context
	done    chan struct{}
	exitErr error
	scratch string
}

// Supervisor owns at most one worker process.
type Supervisor struct {
	opt    Options
	callMu sync.Mutex // one Core→worker call at a time (ADR-0003)
	mu     sync.Mutex
	p      *proc
	gen    int64
	last   time.Time
	busy   bool
	stopCh chan struct{}
	closed bool
}

func New(opt Options) *Supervisor {
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 60 * time.Second
	}
	if opt.Grace <= 0 {
		opt.Grace = 3 * time.Second
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	s := &Supervisor{opt: opt, stopCh: make(chan struct{})}
	go s.idleLoop()
	return s
}

// State reports stopped | idle | busy and the current pid.
func (s *Supervisor) State() (state string, pid int, gen int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return "stopped", 0, s.gen
	}
	select {
	case <-s.p.done:
		return "stopped", 0, s.p.gen
	default:
	}
	if s.busy {
		return "busy", s.p.cmd.Process.Pid, s.p.gen
	}
	return "idle", s.p.cmd.Process.Pid, s.p.gen
}

// Ensure starts the worker if needed and returns its generation. Grants are
// bound to this generation, so a restarted worker cannot reuse old tokens.
func (s *Supervisor) Ensure() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrStopped
	}
	if s.p != nil {
		select {
		case <-s.p.done:
			s.p = nil
		default:
			return s.p.gen, nil
		}
	}
	return s.spawnLocked()
}

func (s *Supervisor) env(scratch string) []string {
	env := []string{"AURA_SCRATCH=" + scratch, "HOME=" + scratch, "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"}
	for _, k := range allowedEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, s.opt.ExtraEnv...)
}

func (s *Supervisor) spawnLocked() (int64, error) {
	s.gen++
	gen := s.gen
	scratch := filepath.Join(s.opt.ScratchRoot, "gen-"+strconv.FormatInt(gen, 10))
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return 0, err
	}
	cmd := exec.Command(s.opt.Command[0], s.opt.Command[1:]...)
	cmd.Env = s.env(scratch)
	cmd.Dir = scratch
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("worker: start: %w", err)
	}
	p := &proc{cmd: cmd, w: rpc.NewWriter(stdin), gen: gen, pending: map[string]chan *rpc.Envelope{},
		done: make(chan struct{}), scratch: scratch}
	s.p = p
	s.last = time.Now()
	s.opt.Log.Info("worker started", "pid", cmd.Process.Pid, "generation", gen)
	go s.logStderr(stderr, gen)
	go s.readLoop(p, stdout)
	return gen, nil
}

func (s *Supervisor) logStderr(r io.Reader, gen int64) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 2000 {
			line = line[:2000]
		}
		s.opt.Log.Info("worker", "generation", gen, "line", obs.Redact(line))
	}
}

func (s *Supervisor) readLoop(p *proc, stdout io.Reader) {
	rd := rpc.NewReader(stdout)
	var exitErr error
	for {
		env, err := rd.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				exitErr = fmt.Errorf("%w: %v", ErrProtocol, err)
				s.opt.Log.Warn("worker protocol error, killing", "generation", p.gen, "err", err)
				killGroup(p.cmd)
			}
			break
		}
		if env.IsRequest() {
			go s.serve(p, env)
			continue
		}
		p.mu.Lock()
		ch := p.pending[env.ID]
		delete(p.pending, env.ID)
		p.mu.Unlock()
		if ch != nil {
			ch <- env
		}
	}
	werr := p.cmd.Wait()
	if exitErr == nil {
		exitErr = werr
	}
	os.RemoveAll(p.scratch)
	p.mu.Lock()
	p.exitErr = exitErr
	// done is closed before pending calls are released so that a caller that
	// sees the crash and immediately calls again never reuses this process.
	close(p.done)
	for id, ch := range p.pending {
		close(ch)
		delete(p.pending, id)
	}
	p.mu.Unlock()
	s.opt.Log.Info("worker exited", "generation", p.gen, "err", exitErr)
}

func (s *Supervisor) serve(p *proc, req *rpc.Envelope) {
	p.mu.Lock()
	h, ctx := p.handler, p.hctx
	p.mu.Unlock()
	resp := &rpc.Envelope{ID: req.ID}
	if h == nil {
		resp.Error = rpc.Errf(rpc.CodeInvalidRequest, "no_active_call", "no active Core call")
	} else {
		res, rerr := h(ctx, req)
		if rerr != nil {
			resp.Error = rerr
		} else {
			b, err := json.Marshal(res)
			if err != nil {
				resp.Error = rpc.Errf(rpc.CodeInternal, "internal", "marshal: %v", err)
			} else {
				resp.Result = b
			}
		}
	}
	if err := p.w.Write(resp); err != nil {
		s.opt.Log.Warn("worker write failed", "err", err)
	}
}

// Call sends a Core→worker request and serves worker→Core requests through h
// until the response arrives. Cancelling ctx kills the worker (its in-flight
// state is unknown) and returns ctx.Err(); the next call spawns a new one.
func (s *Supervisor) Call(ctx context.Context, method string, meta rpc.Meta, params any, h Handler) (json.RawMessage, error) {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	if _, err := s.Ensure(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	p := s.p
	s.busy = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.last = time.Now()
		s.mu.Unlock()
	}()
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := "c-" + ids.New()
	ch := make(chan *rpc.Envelope, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.handler, p.hctx = h, ctx
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.handler, p.hctx = nil, nil
		p.mu.Unlock()
	}()
	if err := p.w.Write(&rpc.Envelope{ID: id, Method: method, Params: b, Meta: &meta}); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCrashed, err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%w: %v", ErrCrashed, p.exitErr)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-p.done:
		return nil, fmt.Errorf("%w: %v", ErrCrashed, p.exitErr)
	case <-ctx.Done():
		s.stopProc(p, false)
		return nil, ctx.Err()
	}
}

// Generation returns the current worker generation (0 if never started).
func (s *Supervisor) Generation() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0
	}
	return s.p.gen
}

// stopProc stops a worker: graceful asks worker.shutdown first; then SIGTERM,
// then SIGKILL after the grace period.
func (s *Supervisor) stopProc(p *proc, graceful bool) {
	defer func() {
		s.mu.Lock()
		if s.p == p {
			s.p = nil
		}
		s.mu.Unlock()
	}()
	if graceful {
		_ = p.w.Write(&rpc.Envelope{ID: "c-" + ids.New(), Method: "worker.shutdown", Params: json.RawMessage(`{"grace_ms":1000}`),
			Meta: &rpc.Meta{TraceID: "shutdown", CorrelationID: "shutdown", DeadlineUnixMs: time.Now().Add(s.opt.Grace).UnixMilli()}})
		select {
		case <-p.done:
			return
		case <-time.After(s.opt.Grace / 2):
		}
	}
	if p.cmd.Process != nil {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-p.done:
	case <-time.After(s.opt.Grace):
		killGroup(p.cmd)
		<-p.done
	}
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Kill stops the current worker immediately (kill switch): SIGTERM, then SIGKILL.
func (s *Supervisor) Kill() {
	s.mu.Lock()
	p := s.p
	s.mu.Unlock()
	if p != nil {
		s.stopProc(p, false)
	}
}

func (s *Supervisor) idleLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.mu.Lock()
			p := s.p
			idle := p != nil && !s.busy && time.Since(s.last) > s.opt.IdleTimeout
			s.mu.Unlock()
			if idle && s.callMu.TryLock() {
				s.opt.Log.Info("worker idle timeout", "generation", p.gen)
				s.stopProc(p, true)
				s.callMu.Unlock()
			}
		}
	}
}

// Close stops the worker gracefully and the idle loop.
func (s *Supervisor) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	p := s.p
	s.mu.Unlock()
	close(s.stopCh)
	if p != nil {
		s.stopProc(p, true)
	}
}

// RSS returns the worker's resident set size in bytes, if running.
func (s *Supervisor) RSS() int64 {
	_, pid, _ := s.State()
	if pid == 0 {
		return 0
	}
	return ProcRSS(pid)
}

// ProcRSS reads VmRSS from /proc/<pid>/status.
func ProcRSS(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	var kb int64
	for _, line := range strings.Split(string(b), "\n") {
		if n, _ := fmt.Sscanf(line, "VmRSS: %d kB", &kb); n == 1 {
			return kb * 1024
		}
	}
	return 0
}
