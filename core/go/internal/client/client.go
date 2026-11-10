// Package client is a typed client of the AURA REST API and event stream,
// used by the Console (ADR-0022). The CLI commands stay thin wrappers.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Task struct {
	ID         string  `json:"id"`
	State      string  `json:"state"`
	Objective  string  `json:"objective"`
	Reason     *string `json:"reason"`
	WaitReason *string `json:"wait_reason"`
	CostUSD    float64 `json:"cost_usd"`
	AgentRole  string  `json:"agent_role"`
	Question   *string `json:"question"`
}

type Approval struct {
	ID         string         `json:"id"`
	TaskID     string         `json:"task_id"`
	ToolID     string         `json:"tool_id"`
	SideEffect string         `json:"side_effect"`
	Target     string         `json:"target"`
	Parameters map[string]any `json:"parameters"`
	ActionHash string         `json:"action_hash"`
	ExpiresAt  string         `json:"expires_at"`
}

type Event struct {
	ID     string          `json:"-"`
	TaskID string          `json:"task_id"`
	Seq    int64           `json:"seq"`
	Type   string          `json:"type"`
	Actor  string          `json:"actor"`
	At     string          `json:"at"`
	Data   json.RawMessage `json:"data"`
}

type Timeline struct {
	Task      Task             `json:"task"`
	NextStep  map[string]any   `json:"next_step"`
	Completed []map[string]any `json:"completed"`
	Blockers  []map[string]any `json:"blockers"`
	Decisions []map[string]any `json:"pending_decisions"`
	Spend     map[string]any   `json:"spend"`
}

type File struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// API is what the console needs; *Client implements it.
type API interface {
	Tasks() ([]Task, error)
	Timeline(id string) (*Timeline, error)
	Create(objective string, public bool, domains []string) (*Task, error)
	Approvals() ([]Approval, error)
	Approve(id, hash string) error
	Reject(id string) error
	Answer(taskID, answer string) error
	Correct(taskID, text string) error
	Cancel(taskID string) error
	Stop() error
	Files(taskID string) ([]File, error)
	File(taskID, path string) (string, error)
	Stream(ctx context.Context, taskID string, out chan<- Event) error
	// Raw calls any other endpoint (path below /api/v1) and returns its JSON.
	Raw(method, path string, body any) (json.RawMessage, error)
}

type Client struct {
	base, token string
	hc          *http.Client
}

func New(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/") + "/api/v1", token: token, hc: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) req(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r, nil
}

func (c *Client) do(method, path string, body, out any) error {
	r, err := c.req(context.Background(), method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
	}
	if s, ok := out.(*string); ok {
		*s = string(raw)
		return nil
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) Tasks() ([]Task, error) {
	var out struct{ Tasks []Task }
	err := c.do("GET", "/tasks?limit=100", nil, &out)
	return out.Tasks, err
}

func (c *Client) Timeline(id string) (*Timeline, error) {
	var t Timeline
	err := c.do("GET", "/tasks/"+id+"/timeline", nil, &t)
	return &t, err
}

func (c *Client) Create(objective string, public bool, domains []string) (*Task, error) {
	body := map[string]any{"objective": objective, "allow_domains": domains}
	if public {
		body["data_class"] = "public"
	}
	var t Task
	err := c.do("POST", "/tasks", body, &t)
	return &t, err
}

func (c *Client) Approvals() ([]Approval, error) {
	var out struct{ Approvals []Approval }
	err := c.do("GET", "/approvals", nil, &out)
	return out.Approvals, err
}

func (c *Client) Approve(id, hash string) error {
	return c.do("POST", "/approvals/"+id+"/approve", map[string]string{"action_hash": hash}, nil)
}
func (c *Client) Reject(id string) error { return c.do("POST", "/approvals/"+id+"/reject", nil, nil) }
func (c *Client) Answer(taskID, answer string) error {
	return c.do("POST", "/tasks/"+taskID+"/input", map[string]string{"answer": answer}, nil)
}
func (c *Client) Correct(taskID, text string) error {
	return c.do("POST", "/tasks/"+taskID+"/context", map[string]string{"content": text}, nil)
}
func (c *Client) Cancel(taskID string) error {
	return c.do("POST", "/tasks/"+taskID+"/cancel", nil, nil)
}
func (c *Client) Stop() error { return c.do("POST", "/system/stop", nil, nil) }

func (c *Client) Raw(method, path string, body any) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(method, path, body, &out)
	return out, err
}

func (c *Client) Files(taskID string) ([]File, error) {
	var out struct{ Files []File }
	err := c.do("GET", "/tasks/"+taskID+"/files", nil, &out)
	return out.Files, err
}

func (c *Client) File(taskID, path string) (string, error) {
	var s string
	err := c.do("GET", "/tasks/"+taskID+"/files/"+(&url.URL{Path: path}).EscapedPath(), nil, &s)
	return s, err
}

// Stream reads Server-Sent Events for a task (from its first event) until ctx
// ends or the connection drops.
func (c *Client) Stream(ctx context.Context, taskID string, out chan<- Event) error {
	r, err := c.req(ctx, "GET", "/stream?after=0&task_id="+url.QueryEscape(taskID), nil)
	if err != nil {
		return err
	}
	hc := &http.Client{} // no overall timeout for a stream
	resp, err := hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("stream: HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var id string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = line[4:]
		case strings.HasPrefix(line, "data: "):
			var ev Event
			if json.Unmarshal([]byte(line[6:]), &ev) == nil {
				ev.ID = id
				select {
				case out <- ev:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
	return sc.Err()
}
