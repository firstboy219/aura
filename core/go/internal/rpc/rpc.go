// Package rpc is the newline-delimited JSON-RPC 2.0 codec between Core and
// worker (ADR-0003). Requests and responses share one envelope type.
package rpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxMessage is the largest accepted line; larger messages are refused, not truncated.
const MaxMessage = 4 << 20

// Error codes.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeDenied         = -32001
	CodeBudget         = -32002
	CodeProvider       = -32003
	CodeHalted         = -32004
)

type Meta struct {
	TaskID           string `json:"task_id,omitempty"`
	StepID           string `json:"step_id,omitempty"`
	TraceID          string `json:"trace_id"`
	CorrelationID    string `json:"correlation_id"`
	CapabilityToken  string `json:"capability_token,omitempty"`
	DeadlineUnixMs   int64  `json:"deadline_unix_ms"`
	CancelGeneration int64  `json:"cancel_generation,omitempty"`
}

type Error struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// Errf builds an error with a reason code.
func Errf(code int, reason, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Data: map[string]any{"reason_code": reason}}
}

type Usage struct {
	InputTokens  int      `json:"input_tokens,omitempty"`
	OutputTokens int      `json:"output_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	LatencyMs    int64    `json:"latency_ms,omitempty"`
}

type Envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Meta    *Meta           `json:"meta,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	Usage   *Usage          `json:"usage,omitempty"`
}

// IsRequest reports whether the envelope is a request.
func (e *Envelope) IsRequest() bool { return e.Method != "" }

var ErrTooLarge = errors.New("rpc message too large")

// Reader reads envelopes, one per line.
type Reader struct{ sc *bufio.Scanner }

func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxMessage)
	return &Reader{sc: sc}
}

// Read returns the next envelope. Oversized lines return ErrTooLarge.
func (r *Reader) Read() (*Envelope, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			if errors.Is(err, bufio.ErrTooLong) {
				return nil, ErrTooLarge
			}
			return nil, err
		}
		return nil, io.EOF
	}
	var e Envelope
	dec := json.NewDecoder(bytes.NewReader(r.sc.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("rpc: malformed message: %w", err)
	}
	if e.JSONRPC != "2.0" || e.ID == "" {
		return nil, errors.New("rpc: missing jsonrpc 2.0 or id")
	}
	return &e, nil
}

// Writer writes envelopes, one per line, safely from multiple goroutines.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

func (w *Writer) Write(e *Envelope) error {
	e.JSONRPC = "2.0"
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) >= MaxMessage {
		return ErrTooLarge
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err = w.w.Write(append(b, '\n'))
	return err
}
