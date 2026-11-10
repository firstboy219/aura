// Package workerhost runs the Python worker in its own container (Blueprint P4,
// ADR-0035): an unprivileged identity, no network at all (network_mode: none),
// no database, workspace or secret mounts. The Core reaches it through a Unix
// socket on a volume shared only by the two containers.
//
// Host side (`aura worker-host`): accept one connection per worker generation,
// start the worker with its stdio on the connection, kill it when the
// connection ends. Core side (`aura worker-connect`): a stdio↔socket bridge
// that the existing worker supervisor runs as its worker command, so process
// supervision, generations and kill semantics stay unchanged.
package workerhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Options configure the host.
type Options struct {
	Socket      string   // Unix socket path
	Command     []string // worker command, e.g. python3 -I -m aura_worker
	ScratchRoot string   // per-connection scratch directories (tmpfs)
	AllowUID    int      // only this peer uid may connect (-1 = any; tests)
	MaxConns    int      // concurrent workers (default 2: one live, one being replaced)
	Grace       time.Duration
	Log         *slog.Logger
}

// allowedEnv are the only variables a worker inherits from the host.
var allowedEnv = []string{"PATH", "LANG", "LC_ALL", "TZ", "PYTHONPATH", "VIRTUAL_ENV"}

// Serve listens until ctx ends.
func Serve(ctx context.Context, o Options) error {
	if o.MaxConns <= 0 {
		o.MaxConns = 2
	}
	if o.Grace <= 0 {
		o.Grace = 3 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if err := os.MkdirAll(o.ScratchRoot, 0o700); err != nil {
		return err
	}
	os.Remove(o.Socket)
	ln, err := net.Listen("unix", o.Socket)
	if err != nil {
		return err
	}
	// The socket lives on a volume only the Core and worker containers mount;
	// the peer uid check below is the authorization.
	if err := os.Chmod(o.Socket, 0o666); err != nil {
		ln.Close()
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	o.Log.Info("worker host listening", "socket", o.Socket, "allow_uid", o.AllowUID)
	slots := make(chan struct{}, o.MaxConns)
	var n int64
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		uc := c.(*net.UnixConn)
		uid, err := peerUID(uc)
		if err != nil || (o.AllowUID >= 0 && uid != o.AllowUID) {
			o.Log.Warn("worker host: connection refused", "peer_uid", uid, "err", err)
			c.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			o.Log.Warn("worker host: too many workers, refusing connection")
			c.Close()
			continue
		}
		n++
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			defer func() { <-slots }()
			if err := runWorker(ctx, o, uc, id); err != nil {
				o.Log.Info("worker ended", "conn", id, "err", err)
			}
		}(n)
	}
}

func peerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, err
	}
	if cerr != nil {
		return -1, cerr
	}
	return int(cred.Uid), nil
}

// runWorker starts one worker with its stdin/stdout on the connection and
// stops it (SIGTERM, then SIGKILL) when the connection or the host ends.
func runWorker(ctx context.Context, o Options, c *net.UnixConn, id int64) error {
	defer c.Close()
	scratch := filepath.Join(o.ScratchRoot, "conn-"+strconv.FormatInt(id, 10))
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Env = []string{"AURA_SCRATCH=" + scratch, "HOME=" + scratch, "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1"}
	for _, k := range allowedEnv {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Dir = scratch
	cmd.Stderr = &logWriter{log: o.Log, conn: id}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	o.Log.Info("worker started", "conn", id, "pid", cmd.Process.Pid)
	exited := make(chan error, 1)
	disconnected := make(chan struct{})
	go func() {
		io.Copy(stdin, c) // Core → worker; returns when the Core side closes or dies
		stdin.Close()
		close(disconnected)
	}()
	outDone := make(chan struct{})
	go func() {
		io.Copy(c, stdout) // worker → Core
		c.CloseWrite()
		close(outDone)
	}()
	go func() {
		<-outDone // Wait closes the pipe: read everything first
		exited <- cmd.Wait()
	}()
	// A disconnect (Core shut the worker down, restarted it, or hit the kill
	// switch) ends the worker even if it is busy and not reading its stdin.
	select {
	case err := <-exited:
		return err
	case <-disconnected:
	case <-ctx.Done():
	}
	select {
	case err := <-exited: // it saw EOF and exited by itself
		return err
	case <-time.After(o.Grace / 3):
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case err := <-exited:
		return err
	case <-time.After(o.Grace):
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return <-exited
	}
}

type logWriter struct {
	log  *slog.Logger
	conn int64
}

func (w *logWriter) Write(p []byte) (int, error) {
	s := string(p)
	if len(s) > 2000 {
		s = s[:2000]
	}
	w.log.Info("worker", "conn", w.conn, "line", s)
	return len(p), nil
}

// Connect bridges stdin/stdout to the worker host socket until either side
// ends. The worker supervisor runs it as the worker command: killing this
// process closes the connection, which ends the remote worker.
func Connect(socket string, stdin io.Reader, stdout io.Writer) error {
	c, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return fmt.Errorf("worker host unreachable: %w", err)
	}
	uc := c.(*net.UnixConn)
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(uc, stdin)
		uc.CloseWrite() // the worker sees EOF on stdin
		done <- err
	}()
	go func() {
		_, err := io.Copy(stdout, uc)
		done <- err
	}()
	err = <-done
	uc.Close()
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
