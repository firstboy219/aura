// Package browser is the Core's client for the isolated browser host
// (Blueprint 8.8.1, ADR-0039): one JSON object per line over a Unix socket,
// one command at a time, every call bounded by a deadline.
package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnavailable means the browser host cannot be reached.
var ErrUnavailable = errors.New("browser_unavailable")

// Error is a browser host error (code + message).
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Client talks to the browser host.
type Client struct {
	// MaxWait bounds one call when the context allows longer (default 60 s; shell commands set their own).
	MaxWait time.Duration
	socket  string
	mu      sync.Mutex
	conn    net.Conn
	rd      *bufio.Reader
	seq     atomic.Int64
}

func New(socket string) *Client { return &Client{socket: socket} }

func (c *Client) dial() error {
	if c.conn != nil {
		return nil
	}
	conn, err := net.DialTimeout("unix", c.socket, 3*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	c.conn, c.rd = conn, bufio.NewReaderSize(conn, 1<<20)
	return nil
}

// Close ends the connection; the next call dials again.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reset()
}

func (c *Client) reset() {
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn, c.rd = nil, nil
}

// Call sends one command and decodes its result into out (if non-nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	reused := c.conn != nil
	if err := c.dial(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	maxWait := c.MaxWait
	if maxWait <= 0 {
		maxWait = 60 * time.Second
	}
	if !ok || time.Until(deadline) > maxWait {
		deadline = time.Now().Add(maxWait)
	}
	c.conn.SetDeadline(deadline)
	id := fmt.Sprint(c.seq.Add(1))
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		c.reset()
		if !reused {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		// The host restarted since the last call: the command was never
		// delivered, so one retry on a fresh connection cannot run it twice.
		if err := c.dial(); err != nil {
			return err
		}
		c.conn.SetDeadline(deadline)
		if _, err := c.conn.Write(append(b, '\n')); err != nil {
			c.reset()
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	line, err := c.rd.ReadBytes('\n')
	if err != nil {
		c.reset() // a timed-out command leaves the stream unusable
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var resp struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil || resp.ID != id {
		c.reset()
		return fmt.Errorf("%w: bad response", ErrUnavailable)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Element is one interactive element from page.inspect.
type Element struct {
	Ref  string `json:"ref"`
	Tag  string `json:"tag"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
	Type string `json:"type,omitempty"`
	Href string `json:"href,omitempty"`
}

// PageState is returned after navigation and actions.
type PageState struct {
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Status      *int      `json:"status"`
	SiteBlocked bool      `json:"site_blocked"`
	Text        string    `json:"text,omitempty"`
	Elements    []Element `json:"elements,omitempty"`
	Generation  int       `json:"generation,omitempty"`
}
