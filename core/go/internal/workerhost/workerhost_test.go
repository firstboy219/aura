package workerhost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/rpc"
	"aura.local/core/internal/worker"
)

// The test binary doubles as the Core-side connector (aura worker-connect).
func TestMain(m *testing.M) {
	if sock := os.Getenv("AURA_TEST_CONNECT"); sock != "" {
		if err := Connect(sock, os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startHost(t *testing.T, command []string, allowUID int) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "w.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Serve(ctx, Options{Socket: sock, Command: command, ScratchRoot: filepath.Join(dir, "scratch"), AllowUID: allowUID,
			Grace: 600 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("host did not start")
	return ""
}

func supervisor(t *testing.T, sock string) *worker.Supervisor {
	t.Helper()
	s := worker.New(worker.Options{Command: []string{os.Args[0]}, ScratchRoot: t.TempDir(), Grace: 500 * time.Millisecond,
		ExtraEnv: []string{"AURA_TEST_CONNECT=" + sock}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(s.Close)
	return s
}

func meta() rpc.Meta {
	return rpc.Meta{TraceID: "t", CorrelationID: "c", DeadlineUnixMs: time.Now().Add(10 * time.Second).UnixMilli()}
}

// The unchanged worker supervisor talks to an isolated worker through the
// socket: health works, and a restart gives a fresh worker process.
func TestSupervisorThroughWorkerHost(t *testing.T) {
	sock := startHost(t, []string{"python3", "-I", "-m", "aura_worker"}, os.Getuid())
	s := supervisor(t, sock)
	raw, err := s.Call(context.Background(), "worker.health", meta(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var h struct{ Status string }
	json.Unmarshal(raw, &h)
	if h.Status != "ok" {
		t.Fatalf("health %s", raw)
	}
	g1 := s.Generation()
	s.Kill()
	if _, err := s.Call(context.Background(), "worker.health", meta(), map[string]any{}, nil); err != nil || s.Generation() == g1 {
		t.Fatalf("restart through the host failed: %v gen %d→%d", err, g1, s.Generation())
	}
}

func running(marker string) bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(string(b), marker) {
			return true
		}
	}
	return false
}

// A busy worker that ignores its stdin is still stopped when the Core side
// disconnects (kill switch, restart).
func TestDisconnectKillsBusyWorker(t *testing.T) {
	marker := "time.sleep(61.2345)"
	sock := startHost(t, []string{"python3", "-c", "import time; " + marker}, os.Getuid())
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !running(marker) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !running(marker) {
		t.Fatal("worker did not start")
	}
	c.Close()
	deadline = time.Now().Add(3 * time.Second)
	for running(marker) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if running(marker) {
		t.Fatal("worker survived the disconnect")
	}
}

// Only the Core's uid may start a worker.
func TestOtherUIDRefused(t *testing.T) {
	marker := "time.sleep(62.3456)"
	sock := startHost(t, []string{"python3", "-c", "import time; " + marker}, os.Getuid()+12345)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection from another uid was served")
	}
	if running(marker) {
		t.Fatal("a worker started for another uid")
	}
}
