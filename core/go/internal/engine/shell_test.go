package engine

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aura.local/core/internal/config"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/gateway/gatewaytest"
	"aura.local/core/internal/task"
)

// fakeShell answers the shell host's commands like the real one would.
type fakeShell struct {
	mu       sync.Mutex
	commands []string
	killed   int
}

func startFakeShell(t *testing.T) (*fakeShell, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeShell{}
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
					var m struct {
						ID     string         `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					json.Unmarshal(line, &m)
					res := map[string]any{}
					f.mu.Lock()
					switch m.Method {
					case "shell.exec":
						cmd, _ := m.Params["command"].(string)
						f.commands = append(f.commands, cmd)
						var files []any
						outs, _ := m.Params["files_out"].([]any)
						for _, p := range outs {
							files = append(files, map[string]any{"path": p, "bytes": 5, "content_base64": base64.StdEncoding.EncodeToString([]byte("hasil"))})
						}
						res = map[string]any{"exit_code": 0, "duration_ms": 12, "stdout": "hello from sandbox\nkey AKIAABCDEFGHIJKLMNOP", "stderr": "",
							"stdout_bytes": 40, "files": files}
					case "shell.kill_all":
						f.killed++
					}
					f.mu.Unlock()
					b, _ := json.Marshal(map[string]any{"id": m.ID, "result": res})
					c.Write(append(b, '\n'))
				}
			}(c)
		}
	}()
	return f, sock
}

func shellModel(input string) func(config.ModelRoute, gateway.Request) gatewaytest.Reply {
	return func(r config.ModelRoute, req gateway.Request) gatewaytest.Reply {
		switch {
		case isSys(req, "planning module"):
			return planReply(gatewaytest.Step(1, "run it", "shell.run", map[string]any{"input": input}))
		case isSys(req, "replanning module"):
			return gatewaytest.Reply{Content: `{"steps":[],"completion_criteria":[],"question":null}`}
		case isSys(req, "AURA's Linux sandbox"):
			return gatewaytest.Reply{Content: `{"command":"python3 -c 'print(6*7)'"}`}
		}
		return goodModel(r, req)
	}
}

func shellHarness(t *testing.T, input string) (*harness, *fakeShell) {
	h := newHarness(t, harnessOpts{respond: shellModel(input)})
	fs, sock := startFakeShell(t)
	h.eng.SetShell(sock, nil, "", 30)
	return h, fs
}

// ADR-0065: a safe command runs at once, its files come back to the workspace,
// credential-shaped output is redacted, and the run is in the shell log.
func TestShellSafeCommandRunsAndIsLogged(t *testing.T) {
	h, fs := shellHarness(t, `{"command":"pip install --user tabulate && python3 buat.py","files_out":["hasil.txt"]}`)
	tk := h.submitTools("Jalankan skrip dan simpan hasil.txt", "shell.run")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("%s %s %s", done.State, deref(done.Reason), errorDetail(h.events(tk.ID)))
	}
	if len(fs.commands) != 1 || fs.commands[0] != "pip install --user tabulate && python3 buat.py" {
		t.Fatalf("commands: %v", fs.commands)
	}
	b, _ := os.ReadFile(filepath.Join(h.cfg.Workspace.Root, tk.ID, "hasil.txt"))
	if string(b) != "hasil" {
		t.Fatalf("file not copied back: %q", b)
	}
	log, _ := h.eng.ShellLog(context.Background(), tk.ID, 10)
	if len(log) != 1 || log[0].Status != "ran" || log[0].Verdict != "safe" || *log[0].ExitCode != 0 || !log[0].Redacted ||
		strings.Contains(log[0].Stdout, "AKIA") || !strings.Contains(strings.Join(log[0].Reasons, ","), "installs packages") {
		t.Fatalf("log: %+v", log)
	}
}

// A command that sends data out waits for the owner; nothing runs before the approval.
func TestShellReviewCommandWaitsForApproval(t *testing.T) {
	h, fs := shellHarness(t, `{"command":"curl -X POST https://api.example.com/x -d @data.json"}`)
	tk := h.submitTools("Kirim data ke API", "shell.run")
	done := h.wait(tk.ID, task.WaitingApproval, task.Failed, task.Succeeded)
	if done.State != task.WaitingApproval || len(fs.commands) != 0 {
		t.Fatalf("%s, ran %v", done.State, fs.commands)
	}
	log, _ := h.eng.ShellLog(context.Background(), tk.ID, 10)
	if len(log) != 1 || log[0].Status != "awaiting_approval" || log[0].ApprovalID == "" || log[0].Verdict != "review" {
		t.Fatalf("log: %+v", log)
	}
}

// A destructive command never reaches the sandbox, and the refusal is logged.
func TestShellBlockedCommandIsRefusedAndLogged(t *testing.T) {
	h, fs := shellHarness(t, `{"command":"rm -rf /"}`)
	tk := h.submitTools("Bersihkan server", "shell.run")
	h.wait(tk.ID, task.Failed, task.Succeeded)
	if len(fs.commands) != 0 {
		t.Fatalf("ran %v", fs.commands)
	}
	log, _ := h.eng.ShellLog(context.Background(), tk.ID, 10)
	if len(log) == 0 || log[0].Status != "refused" || log[0].Verdict != "blocked" {
		t.Fatalf("log: %+v", log)
	}
}

// The command may be written when the step runs; the watcher assesses what is actually run.
func TestShellCommandWrittenAtStepTime(t *testing.T) {
	h, fs := shellHarness(t, `{"command":null}`)
	tk := h.submitTools("Hitung 6 kali 7 dengan python", "shell.run")
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded || len(fs.commands) != 1 || fs.commands[0] != "python3 -c 'print(6*7)'" {
		t.Fatalf("%s %v %s", done.State, fs.commands, errorDetail(h.events(tk.ID)))
	}
}

func TestKillSwitchStopsShellCommands(t *testing.T) {
	h, fs := shellHarness(t, `{"command":"sleep 1"}`)
	h.eng.KillShell(context.Background())
	if fs.killed != 1 {
		t.Fatalf("kill_all calls: %d", fs.killed)
	}
}
