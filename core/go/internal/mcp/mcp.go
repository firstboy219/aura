// Package mcp is a minimal Model Context Protocol client over stdio
// (ADR-0020): initialize, notifications/initialized, tools/list, tools/call.
// Servers are owner-declared child processes; their tool list and descriptions
// are untrusted metadata and never change a tool's tier or side-effect class.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const protocolVersion = "2025-06-18"

var (
	ErrServer  = errors.New("mcp_server_error")
	ErrTimeout = errors.New("mcp_timeout")
)

// ToolInfo is what a server reports about one tool (untrusted).
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// CallResult is the text content of a tools/call result.
type CallResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Client talks to one MCP server process, started on first use and restarted
// after it exits.
type Client struct {
	Name    string
	command []string
	env     []string
	timeout time.Duration
	log     *slog.Logger

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	nextID  int64
	pending map[int64]chan message
	done    chan struct{}
	tools   map[string]ToolInfo
}

// New creates a client. env is the complete environment of the server process.
func New(name string, command, env []string, timeout time.Duration, log *slog.Logger) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Client{Name: name, command: command, env: env, timeout: timeout, log: log}
}

func (c *Client) start(ctx context.Context) error {
	if c.cmd != nil {
		select {
		case <-c.done:
			c.cmd = nil
		default:
			return nil
		}
	}
	cmd := exec.Command(c.command[0], c.command[1:]...)
	cmd.Env = c.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: start %s: %v", ErrServer, c.Name, err)
	}
	c.cmd, c.stdin, c.pending, c.done = cmd, stdin, map[int64]chan message{}, make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			c.log.Info("mcp server", "server", c.Name, "line", truncate(sc.Text(), 500))
		}
	}()
	go c.readLoop(stdout, c.done, c.pending)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := c.callLocked(ctx, "initialize", map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "aura", "version": "0.2"}}, &init); err != nil {
		c.killLocked()
		return err
	}
	if err := c.writeLocked(message{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		c.killLocked()
		return err
	}
	var list struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := c.callLocked(ctx, "tools/list", map[string]any{}, &list); err != nil {
		c.killLocked()
		return err
	}
	c.tools = map[string]ToolInfo{}
	for _, t := range list.Tools {
		c.tools[t.Name] = t
	}
	c.log.Info("mcp server started", "server", c.Name, "tools", len(c.tools), "protocol", init.ProtocolVersion)
	return nil
}

func (c *Client) readLoop(r io.Reader, done chan struct{}, pending map[int64]chan message) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil || m.Method != "" {
			continue // notifications and server requests are ignored (no sampling/roots support)
		}
		c.mu.Lock()
		ch := pending[*m.ID]
		delete(pending, *m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.mu.Lock()
	for id, ch := range pending {
		close(ch)
		delete(pending, id)
	}
	c.mu.Unlock()
	close(done)
}

func (c *Client) writeLocked(m message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// callLocked sends a request and waits for its response. c.mu must be held;
// it is released while waiting.
func (c *Client) callLocked(ctx context.Context, method string, params, out any) error {
	c.nextID++
	id := c.nextID
	ch := make(chan message, 1)
	c.pending[id] = ch
	done := c.done
	if err := c.writeLocked(message{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		delete(c.pending, id)
		return fmt.Errorf("%w: write: %v", ErrServer, err)
	}
	c.mu.Unlock()
	defer c.mu.Lock()
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("%w: %s exited", ErrServer, c.Name)
		}
		if m.Error != nil {
			return fmt.Errorf("%w: %s: %s", ErrServer, method, truncate(m.Error.Message, 300))
		}
		if out != nil {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-done:
		return fmt.Errorf("%w: %s exited", ErrServer, c.Name)
	case <-timer.C:
		return fmt.Errorf("%w: %s %s", ErrTimeout, c.Name, method)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Tools starts the server if needed and returns its reported tools.
func (c *Client) Tools(ctx context.Context) (map[string]ToolInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.start(ctx); err != nil {
		return nil, err
	}
	return c.tools, nil
}

// Call invokes a tool and returns its text content.
func (c *Client) Call(ctx context.Context, tool string, args map[string]any) (*CallResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.start(ctx); err != nil {
		return nil, err
	}
	if _, ok := c.tools[tool]; !ok {
		return nil, fmt.Errorf("%w: %s does not offer tool %q", ErrServer, c.Name, tool)
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.callLocked(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &res); err != nil {
		if errors.Is(err, ErrTimeout) || errors.Is(err, context.Canceled) {
			c.killLocked() // the server's state is unknown after a timeout
		}
		return nil, err
	}
	out := &CallResult{IsError: res.IsError}
	for _, p := range res.Content {
		if p.Type == "text" {
			out.Text += p.Text
		} else {
			out.Text += fmt.Sprintf("[%s content omitted]", p.Type)
		}
	}
	return out, nil
}

func (c *Client) killLocked() {
	if c.cmd != nil && c.cmd.Process != nil {
		syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		done := c.done
		c.mu.Unlock()
		<-done
		c.cmd.Wait()
		c.mu.Lock()
		c.cmd = nil
	}
}

// Close stops the server.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		c.stdin.Close()
		c.killLocked()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
