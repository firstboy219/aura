package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func serveOnce(t *testing.T, sock string, calls *atomic.Int32) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rd := bufio.NewReader(c)
				for {
					line, err := rd.ReadBytes('\n')
					if err != nil {
						return
					}
					var m map[string]any
					json.Unmarshal(line, &m)
					calls.Add(1)
					b, _ := json.Marshal(map[string]any{"id": m["id"], "result": map[string]any{"status": "ok"}})
					c.Write(append(b, '\n'))
				}
			}(c)
		}
	}()
	return ln
}

// A restarted browser host is reached again on the next call, which runs once.
func TestReconnectAfterHostRestart(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "b.sock")
	var calls atomic.Int32
	ln := serveOnce(t, sock, &calls)
	c := New(sock)
	ctx := context.Background()
	if err := c.Call(ctx, "host.health", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	ln.Close() // host restarts: the old connection is gone
	c.mu.Lock()
	if uc, ok := c.conn.(*net.UnixConn); ok {
		uc.CloseRead() // the peer side vanished
	}
	c.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	ln2 := serveOnce(t, sock+"2", &calls)
	defer ln2.Close()
	c.socket = sock + "2"
	before := calls.Load()
	if err := c.Call(ctx, "host.health", map[string]any{}, nil); err != nil {
		// A read failure after a delivered write is not retried (outcome unknown);
		// the following call must succeed on a fresh connection.
		if err := c.Call(ctx, "host.health", map[string]any{}, nil); err != nil {
			t.Fatalf("no recovery after host restart: %v", err)
		}
	}
	if got := calls.Load() - before; got < 1 || got > 2 {
		t.Fatalf("calls after restart: %d", got)
	}
}
