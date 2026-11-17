// Package nexus is AURA's side of the NEXUS contract (ODI AURA Blueprint v1.1 §7, ADR-0073): a client for the cognitive API, objective health evidence with
// hysteresis, AURA-owned fallback authorization, the cognition router the gateway delegates to, and the Teacher Gate.
// NEXUS never holds provider keys; an external provider can serve cognition only through a FallbackAuthorization issued here.
package nexus

import (
	"errors"
	"fmt"
	"strings"
)

// Request mirrors contracts/odi/nexus/cognitive-request.schema.json.
type Request struct {
	RequestID      string `json:"request_id"`
	TaskID         string `json:"task_id"`
	SessionID      string `json:"session_id"`
	Caller         Caller `json:"caller"`
	Purpose        string `json:"purpose"`
	Scope          Scope  `json:"scope"`
	Classification string `json:"classification"`
	Deadline       string `json:"deadline"`
	IdempotencyKey string `json:"idempotency_key"`
	Input          Input  `json:"input"`
	Context        Ctx    `json:"context"`
	SchemaVersion  string `json:"schema_version"`
}

type Caller struct {
	Platform  string `json:"platform"`
	ServiceID string `json:"service_id"`
}
type Scope struct {
	WorkspaceID      string   `json:"workspace_id"`
	AllowedKnowledge []string `json:"allowed_knowledge"`
}
type Input struct {
	Text         string   `json:"text"`
	EvidenceRefs []string `json:"evidence_refs"`
}
type Ctx struct {
	ContextID   string `json:"context_id"`
	TokenBudget int    `json:"token_budget"`
}

// Result mirrors cognitive-result.schema.json (only the fields AURA uses).
type Result struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Result    struct {
		Content     string   `json:"content"`
		Limitations []string `json:"limitations"`
		Unknowns    []string `json:"unknowns"`
	} `json:"result"`
	Confidence struct {
		Band   string `json:"band"`
		Method string `json:"method"`
	} `json:"confidence"`
	ExecutionAuthority string `json:"execution_authority"`
	Model              struct {
		ModelID string `json:"model_id"`
		Version string `json:"version"`
		Runtime string `json:"runtime"`
	} `json:"model"`
	KnowledgeReleaseID string `json:"knowledge_release_id"`
	TraceID            string `json:"trace_id"`
	SchemaVersion      string `json:"schema_version"`
}

// Terminal says whether the request reached an end state.
func (r *Result) Terminal() bool {
	switch r.Status {
	case "SUCCEEDED", "INSUFFICIENT_EVIDENCE", "FAILED", "CANCELLED", "EXPIRED", "REJECTED":
		return true
	}
	return false
}

// Error is a NEXUS error envelope (contracts error-envelope.schema.json) or a transport failure (Code "TRANSPORT").
type Error struct {
	Code      string
	Message   string
	Retryable bool
	HTTP      int
}

func (e *Error) Error() string {
	return fmt.Sprintf("nexus %s (HTTP %d): %s", e.Code, e.HTTP, e.Message)
}

// Errors the router returns to the gateway's callers.
var (
	ErrFallbackNotAuthorized = errors.New("FALLBACK_NOT_AUTHORIZED")
	ErrNexusUnavailable      = errors.New("NEXUS_UNAVAILABLE")
	ErrNexusHealthUnknown    = errors.New("NEXUS_HEALTH_UNKNOWN")
	ErrDataClassNotAllowed   = errors.New("DATA_CLASS_NOT_ALLOWED")
	ErrProtocol              = errors.New("nexus_protocol_violation")
	ErrInsufficientEvidence  = errors.New("INSUFFICIENT_EVIDENCE")
)

// UnavailabilityEvidence reports whether a NEXUS error code is objective evidence that the cognitive path is broken. Capacity, latency, queue depth,
// missing knowledge and bad requests are NOT (Blueprint §7: they are never a trigger for external AI).
func UnavailabilityEvidence(code string) bool {
	switch code {
	case "MODEL_UNAVAILABLE", "KNOWLEDGE_UNAVAILABLE", "INTERNAL_ERROR", "TRANSPORT":
		return true
	}
	return false
}

// ContextOverflowMarker is what NEXUS says when an input cannot fit its context (INVALID_REQUEST, not retryable). It is a capacity limit, not a fault.
const ContextOverflowMarker = "does not fit the model context"

// IsCapacityExceeded: NEXUS cannot take this task (context too small, or not finished in time), as opposed to being unavailable.
func IsCapacityExceeded(err error) bool {
	var ne *Error
	if !errors.As(err, &ne) {
		return false
	}
	return (ne.Code == "INVALID_REQUEST" && strings.Contains(ne.Message, ContextOverflowMarker)) || ne.Code == "DEADLINE_EXCEEDED"
}
