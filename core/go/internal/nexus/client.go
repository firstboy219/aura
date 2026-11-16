package nexus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client speaks the NEXUS cognitive API (contracts/odi/nexus). AURA is the only caller NEXUS accepts (Blueprint §2.2); the token is AURA's service
// identity for NEXUS (vault secret "nexus_service_token"), never a model-provider key.
type Client struct {
	BaseURL func() string
	Token   func() string
	HTTP    *http.Client
	Poll    time.Duration
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (int, error) {
	base := strings.TrimRight(c.BaseURL(), "/")
	if base == "" {
		return 0, &Error{Code: "TRANSPORT", Message: "NEXUS base_url is not configured"}
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return 0, &Error{Code: "TRANSPORT", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != nil {
		if t := c.Token(); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	resp, err := c.http().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return 0, err
		}
		return 0, &Error{Code: "TRANSPORT", Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		var env struct {
			Error struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		e := &Error{Code: "INTERNAL_ERROR", Message: truncate(string(raw), 200), HTTP: resp.StatusCode}
		if json.Unmarshal(raw, &env) == nil && env.Error.Code != "" {
			e.Code, e.Message, e.Retryable = env.Error.Code, env.Error.Message, env.Error.Retryable
		} else if resp.StatusCode == 503 {
			e.Code = "MODEL_UNAVAILABLE"
		} else if resp.StatusCode == 429 {
			e.Code = "RATE_LIMITED"
		}
		return resp.StatusCode, e
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, &Error{Code: "INTERNAL_ERROR", Message: "unreadable response: " + err.Error(), HTTP: resp.StatusCode}
		}
	}
	return resp.StatusCode, nil
}

// Ready is the bounded active probe of the critical cognitive path (GET /health/ready).
func (c *Client) Ready(ctx context.Context) error {
	_, err := c.do(ctx, "GET", "/health/ready", nil, nil)
	return err
}

// Submit sends a cognitive request and waits for a terminal result until ctx ends. The result is checked: NEXUS output is advisory, so
// execution_authority must be "none" and the request id must match, otherwise the response is rejected as a protocol violation.
func (c *Client) Submit(ctx context.Context, req Request) (*Result, error) {
	var res Result
	if _, err := c.do(ctx, "POST", "/api/v1/cognitive/requests", req, &res); err != nil {
		return nil, err
	}
	poll := c.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	for !res.Terminal() {
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			c.Cancel(cctx, req.RequestID)
			cancel()
			return nil, &Error{Code: "DEADLINE_EXCEEDED", Message: "deadline elapsed while waiting for NEXUS"}
		case <-time.After(poll):
		}
		if _, err := c.do(ctx, "GET", "/api/v1/cognitive/requests/"+req.RequestID, nil, &res); err != nil {
			return nil, err
		}
	}
	if res.ExecutionAuthority != "none" || res.RequestID != req.RequestID {
		return nil, fmt.Errorf("%w: execution_authority=%q request_id match=%v", ErrProtocol, res.ExecutionAuthority, res.RequestID == req.RequestID)
	}
	return &res, nil
}

// Cancel asks NEXUS to stop an in-flight request.
func (c *Client) Cancel(ctx context.Context, id string) error {
	_, err := c.do(ctx, "POST", "/api/v1/cognitive/requests/"+id+"/cancel", map[string]string{}, nil)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
