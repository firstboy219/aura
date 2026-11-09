package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"aura.local/core/internal/rpc"
)

// script is a tiny worker used to exercise supervisor behaviour.
const script = `
import json, os, sys, time
for line in sys.stdin:
    m = json.loads(line)
    mode = (m.get("params") or {}).get("mode")
    if mode == "env":
        r = {"env": dict(os.environ)}
    elif mode == "crash":
        os._exit(3)
    elif mode == "huge":
        sys.stdout.write("x" * (5 << 20) + "\n"); sys.stdout.flush(); continue
    elif mode == "sleep":
        time.sleep(30); r = {}
    elif mode == "ask":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": "w-1", "method": "llm.complete", "params": {"x": 1},
                                     "meta": {"trace_id": "t", "correlation_id": "c", "deadline_unix_ms": 0}}) + "\n")
        sys.stdout.flush()
        resp = json.loads(sys.stdin.readline())
        r = {"core_said": resp.get("result")}
    elif m.get("method") == "worker.shutdown":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": m["id"], "result": {"accepted": True}}) + "\n"); sys.stdout.flush()
        break
    else:
        r = {"pid": os.getpid()}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": m["id"], "result": r}) + "\n"); sys.stdout.flush()
`

func sup(t *testing.T, idle time.Duration) *Supervisor {
	s := New(Options{Command: []string{"python3", "-I", "-c", script}, ScratchRoot: t.TempDir(), IdleTimeout: idle,
		Grace: 500 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(s.Close)
	return s
}

func meta() rpc.Meta { return rpc.Meta{TraceID: "t", CorrelationID: "c", DeadlineUnixMs: 0} }

func call(t *testing.T, s *Supervisor, mode string, h Handler) (map[string]any, error) {
	raw, err := s.Call(context.Background(), "test", meta(), map[string]string{"mode": mode}, h)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m, nil
}

func TestEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-should-never-leak")
	t.Setenv("AURA_SECRET_THING", "x")
	s := sup(t, time.Minute)
	m, err := call(t, s, "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := m["env"].(map[string]any)
	for k := range env {
		switch k {
		case "PATH", "LANG", "LC_ALL", "TZ", "PYTHONPATH", "VIRTUAL_ENV", "AURA_SCRATCH", "HOME", "PYTHONDONTWRITEBYTECODE", "PYTHONUNBUFFERED", "LC_CTYPE":
		default:
			t.Errorf("unexpected env var %s", k)
		}
	}
	if !strings.Contains(env["AURA_SCRATCH"].(string), "gen-1") {
		t.Fatalf("scratch %v", env["AURA_SCRATCH"])
	}
}

func TestCrashRestartsWithNewGeneration(t *testing.T) {
	s := sup(t, time.Minute)
	m1, _ := call(t, s, "pid", nil)
	g1 := s.Generation()
	if _, err := call(t, s, "crash", nil); !errors.Is(err, ErrCrashed) {
		t.Fatalf("crash not reported: %v", err)
	}
	m2, err := call(t, s, "pid", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m1["pid"] == m2["pid"] || s.Generation() == g1 {
		t.Fatal("worker not restarted with a new generation")
	}
}

func TestOversizeMessageKillsWorker(t *testing.T) {
	s := sup(t, time.Minute)
	if _, err := call(t, s, "huge", nil); !errors.Is(err, ErrCrashed) {
		t.Fatalf("oversize accepted: %v", err)
	}
}

func TestWorkerRequestsAreServedByHandler(t *testing.T) {
	s := sup(t, time.Minute)
	h := func(ctx context.Context, req *rpc.Envelope) (any, *rpc.Error) {
		if req.Method != "llm.complete" {
			return nil, rpc.Errf(rpc.CodeMethodNotFound, "x", "no")
		}
		return map[string]string{"content": "hi"}, nil
	}
	m, err := call(t, s, "ask", h)
	if err != nil {
		t.Fatal(err)
	}
	if m["core_said"].(map[string]any)["content"] != "hi" {
		t.Fatalf("%v", m)
	}
}

func TestCancelKillsWorkerQuickly(t *testing.T) {
	s := sup(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := s.Call(ctx, "test", meta(), map[string]string{"mode": "sleep"}, nil)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
	if st, _, _ := s.State(); st != "stopped" {
		t.Fatalf("state %s", st)
	}
}

func TestIdleShutdown(t *testing.T) {
	s := sup(t, time.Second)
	call(t, s, "pid", nil)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _, _ := s.State(); st == "stopped" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("worker not stopped after idle timeout")
}

// The real Python worker answers worker.health (requires the package installed).
func TestRealWorkerHealth(t *testing.T) {
	s := New(Options{Command: []string{"python3", "-I", "-m", "aura_worker"}, ScratchRoot: t.TempDir(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer s.Close()
	raw, err := s.Call(context.Background(), "worker.health", meta(), map[string]any{}, nil)
	if err != nil {
		t.Skipf("aura_worker not installed here: %v", err)
	}
	var h struct{ Status, Version string }
	json.Unmarshal(raw, &h)
	if h.Status != "ok" {
		t.Fatalf("%s", raw)
	}
}
