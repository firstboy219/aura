// Package policy is the Core-side authority for capability grants, the trusted
// tool manifest, approval tiers and approvals (Blueprint §8.3, ADR-0006).
// Nothing here trusts a worker or model declaration.
package policy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/store"
)

type Tier string

const (
	A0 Tier = "A0" // auto: execute and log
	A1 Tier = "A1" // notify: unsupported in P1, always denied
	A2 Tier = "A2" // approve: wait for a human
	A3 Tier = "A3" // forbidden: deny and audit
)

// ToolSpec is a trusted manifest entry compiled into the Core.
type ToolSpec struct {
	ID         string
	Version    string
	SideEffect string // pure | read | write_idempotent | write_external
	// Tier computes the approval tier from validated arguments.
	Tier func(args map[string]any) Tier
}

// ProtectedPrefix is a workspace prefix no tool may write (A3).
const ProtectedPrefix = ".aura/"

// PublishedPrefix is a workspace prefix whose writes require approval (A2).
const PublishedPrefix = "published/"

// Manifest is the Phase 1 trusted tool manifest (E.2 item 7).
var Manifest = map[string]ToolSpec{
	"web.fetch":      {ID: "web.fetch", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"web.search":     {ID: "web.search", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"data.read":      {ID: "data.read", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"calc":           {ID: "calc", Version: "1", SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }},
	"wait.event":     {ID: "wait.event", Version: "1", SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }},
	"wait.until":     {ID: "wait.until", Version: "1", SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }},
	"agent.delegate": {ID: "agent.delegate", Version: "1", SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }},
	// Browser automation v1 (Blueprint 8.8.1, ADR-0039): reading and local page edits are A0 inside the task's domains;
	// a click can submit a form or change data, so it needs an A2 approval bound to the page, element and typed values.
	"browser.open":       {ID: "browser.open", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"browser.inspect":    {ID: "browser.inspect", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"browser.screenshot": {ID: "browser.screenshot", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"browser.scroll":     {ID: "browser.scroll", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"browser.wait":       {ID: "browser.wait", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"browser.back":       {ID: "browser.back", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"browser.type":       {ID: "browser.type", Version: "1", SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }},
	"browser.select":     {ID: "browser.select", Version: "1", SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }},
	"browser.click":      {ID: "browser.click", Version: "1", SideEffect: "write_external", Tier: func(map[string]any) Tier { return A2 }},
	"workspace.read":     {ID: "workspace.read", Version: "1", SideEffect: "read", Tier: func(map[string]any) Tier { return A0 }},
	"workspace.write": {ID: "workspace.write", Version: "1", SideEffect: "write_idempotent", Tier: func(a map[string]any) Tier {
		p, _ := a["path"].(string)
		switch {
		case strings.HasPrefix(p, ProtectedPrefix):
			return A3
		case strings.HasPrefix(p, PublishedPrefix):
			return A2
		}
		return A0
	}},
}

// RegisterMCPTool adds an owner-declared MCP tool to the trusted manifest at
// startup (before any task runs). Its tier and side-effect class come from the
// owner's config, never from the MCP server.
func RegisterMCPTool(id, sideEffect string, tier Tier) {
	if !strings.HasPrefix(id, "mcp.") {
		panic("policy: MCP tool ids start with mcp.")
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	Manifest[id] = ToolSpec{ID: id, Version: "mcp-1", SideEffect: sideEffect, Tier: func(map[string]any) Tier { return tier }}
}

// manifestMu guards Manifest: L2 tools are registered and unregistered at
// runtime (owner approval, rollback) while tasks read it.
var manifestMu sync.RWMutex

// Lookup returns a tool's trusted manifest entry.
func Lookup(id string) (ToolSpec, bool) {
	manifestMu.RLock()
	defer manifestMu.RUnlock()
	s, ok := Manifest[id]
	return s, ok
}

// ToolIDs lists every registered tool id.
func ToolIDs() []string {
	manifestMu.RLock()
	defer manifestMu.RUnlock()
	out := make([]string, 0, len(Manifest))
	for id := range Manifest {
		out = append(out, id)
	}
	return out
}

// RegisterL2Tool makes an owner-approved L2 tool available (Blueprint 8A.1,
// ADR-0038). L2 tools are pure computations in the WASM sandbox: no side
// effect, tier A0. version is the approved version.
func RegisterL2Tool(id string, version int) {
	if !strings.HasPrefix(id, "l2.") {
		panic("policy: L2 tool ids start with l2.")
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	Manifest[id] = ToolSpec{ID: id, Version: fmt.Sprintf("l2-%d", version), SideEffect: "pure", Tier: func(map[string]any) Tier { return A0 }}
}

// UnregisterL2Tool removes an L2 tool (disabled or rolled back).
func UnregisterL2Tool(id string) {
	if !strings.HasPrefix(id, "l2.") {
		return
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	delete(Manifest, id)
}

// Errors returned to callers; all denials are audited by the caller.
var (
	ErrDenied          = errors.New("capability_denied")
	ErrTierUnsupported = errors.New("tier_unsupported")
	ErrForbidden       = errors.New("forbidden")
	ErrApprovalMissing = errors.New("not_found")
	ErrNotPending      = errors.New("approval_not_pending")
	ErrExpired         = errors.New("approval_expired")
	ErrMismatch        = errors.New("approval_mismatch")
)

// CheckTier maps a tier to a decision; A1 is denied in Phase 1, never mapped to A0.
func CheckTier(t Tier) error {
	switch t {
	case A0, A2:
		return nil
	case A1:
		return ErrTierUnsupported
	default:
		return ErrForbidden
	}
}

// Scopes are what a grant allows.
type Scopes struct {
	Tools        []string `json:"tools"`
	AllowDomains []string `json:"allow_domains"`
	PathPrefix   string   `json:"path_prefix"`
}

func (s Scopes) HasTool(id string) bool {
	for _, t := range s.Tools {
		if t == id {
			return true
		}
	}
	return false
}

type Grant struct {
	ID               string
	TaskID           string
	ParentGrantID    *string
	WorkerGeneration int64
	Scopes           Scopes
	ExpiresAt        int64
	RevokedAt        *int64
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// Mint creates an opaque 256-bit token bound to a task and worker generation.
// Only the token's sha256 is stored; the raw token is returned once.
func Mint(ctx context.Context, tx *sql.Tx, taskID string, workerGen int64, sc Scopes, expiresAt int64, parent *string) (token string, g *Grant, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	b, _ := json.Marshal(sc)
	g = &Grant{ID: ids.New(), TaskID: taskID, ParentGrantID: parent, WorkerGeneration: workerGen, Scopes: sc, ExpiresAt: expiresAt}
	_, err = tx.ExecContext(ctx, `insert into capability_grants(id, task_id, parent_grant_id, token_sha256, worker_generation,
		scopes_json, expires_at, created_at) values(?,?,?,?,?,?,?,?)`,
		g.ID, taskID, parent, hashToken(token), workerGen, string(b), expiresAt, ids.NowMs())
	return token, g, err
}

// Verify resolves a presented token to its grant and checks task, worker
// generation, expiry and revocation. It never trusts the token by itself.
func Verify(ctx context.Context, q store.Querier, token, taskID string, workerGen int64) (*Grant, error) {
	if len(token) < 32 {
		return nil, ErrDenied
	}
	want := hashToken(token)
	var g Grant
	var stored, sc string
	err := q.QueryRowContext(ctx, `select id, task_id, parent_grant_id, token_sha256, worker_generation, scopes_json,
		expires_at, revoked_at from capability_grants where token_sha256 = ?`, want).
		Scan(&g.ID, &g.TaskID, &g.ParentGrantID, &stored, &g.WorkerGeneration, &sc, &g.ExpiresAt, &g.RevokedAt)
	if err != nil {
		return nil, ErrDenied
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(want)) != 1 {
		return nil, ErrDenied
	}
	if g.TaskID != taskID || g.WorkerGeneration != workerGen || g.RevokedAt != nil || ids.NowMs() >= g.ExpiresAt {
		return nil, ErrDenied
	}
	if err := json.Unmarshal([]byte(sc), &g.Scopes); err != nil {
		return nil, ErrDenied
	}
	return &g, nil
}

// RevokeTask revokes every live grant of a task.
func RevokeTask(ctx context.Context, tx *sql.Tx, taskID string) error {
	_, err := tx.ExecContext(ctx, "update capability_grants set revoked_at=? where task_id=? and revoked_at is null", ids.NowMs(), taskID)
	return err
}

// RevokeAll revokes every live grant (kill switch).
func RevokeAll(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "update capability_grants set revoked_at=? where revoked_at is null", ids.NowMs())
	return err
}

// Action is the exact thing an approval authorizes.
type Action struct {
	ToolID      string         `json:"tool_id"`
	ToolVersion string         `json:"tool_version"`
	Arguments   map[string]any `json:"arguments"`
	Target      string         `json:"target"`
	SideEffect  string         `json:"side_effect"`
	TaskID      string         `json:"task_id"`
	StepID      string         `json:"step_id"`
	GrantID     string         `json:"grant_id"`
}

// Hash is sha256 over canonical JSON of the action. encoding/json sorts map
// keys at every level, so equal actions always hash equally and any changed
// parameter changes the hash.
func (a Action) Hash() string {
	b, _ := json.Marshal(a)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type Approval struct {
	ID          string         `json:"id"`
	TaskID      string         `json:"task_id"`
	StepID      string         `json:"step_id"`
	GrantID     string         `json:"grant_id"`
	ToolID      string         `json:"tool_id"`
	ToolVersion string         `json:"tool_version"`
	SideEffect  string         `json:"side_effect"`
	Target      string         `json:"target"`
	Parameters  map[string]any `json:"parameters"`
	ActionHash  string         `json:"action_hash"`
	Status      string         `json:"status"`
	ExpiresAt   int64          `json:"expires_at"`
	CreatedAt   int64          `json:"created_at"`
}

// Action reconstructs the approved action from the stored record.
func (ap *Approval) Action() Action {
	return Action{ToolID: ap.ToolID, ToolVersion: ap.ToolVersion, Arguments: ap.Parameters, Target: ap.Target,
		SideEffect: ap.SideEffect, TaskID: ap.TaskID, StepID: ap.StepID, GrantID: ap.GrantID}
}

// RequestApproval stores a pending approval and invalidates any other pending
// approval for the same step (a changed action needs a new approval).
func RequestApproval(ctx context.Context, tx *sql.Tx, a Action, ttlMs int64) (*Approval, error) {
	now := ids.NowMs()
	if _, err := tx.ExecContext(ctx, "update approvals set status='invalidated' where step_id=? and status in ('pending','approved')", a.StepID); err != nil {
		return nil, err
	}
	p, _ := json.Marshal(a.Arguments)
	ap := &Approval{ID: ids.New(), TaskID: a.TaskID, StepID: a.StepID, GrantID: a.GrantID, ToolID: a.ToolID,
		ToolVersion: a.ToolVersion, SideEffect: a.SideEffect, Target: a.Target, Parameters: a.Arguments,
		ActionHash: a.Hash(), Status: "pending", ExpiresAt: now + ttlMs, CreatedAt: now}
	_, err := tx.ExecContext(ctx, `insert into approvals(id, task_id, step_id, grant_id, tool_id, tool_version, side_effect, target,
		params_json, action_hash, status, expires_at, created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ap.ID, ap.TaskID, ap.StepID, ap.GrantID, ap.ToolID, ap.ToolVersion, ap.SideEffect, ap.Target, string(p),
		ap.ActionHash, ap.Status, ap.ExpiresAt, ap.CreatedAt)
	return ap, err
}

const apCols = "id, task_id, step_id, grant_id, tool_id, tool_version, side_effect, target, params_json, action_hash, status, expires_at, created_at"

func scanApproval(sc interface{ Scan(...any) error }) (*Approval, error) {
	var ap Approval
	var p string
	if err := sc.Scan(&ap.ID, &ap.TaskID, &ap.StepID, &ap.GrantID, &ap.ToolID, &ap.ToolVersion, &ap.SideEffect,
		&ap.Target, &p, &ap.ActionHash, &ap.Status, &ap.ExpiresAt, &ap.CreatedAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(p), &ap.Parameters)
	return &ap, nil
}

// GetApproval loads one approval.
func GetApproval(ctx context.Context, q store.Querier, id string) (*Approval, error) {
	ap, err := scanApproval(q.QueryRowContext(ctx, "select "+apCols+" from approvals where id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrApprovalMissing
	}
	return ap, err
}

// Pending lists pending approvals, oldest first.
func Pending(ctx context.Context, q store.Querier) ([]*Approval, error) {
	rows, err := q.QueryContext(ctx, "select "+apCols+" from approvals where status='pending' order by created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Approval{}
	for rows.Next() {
		ap, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ap)
	}
	return out, rows.Err()
}

// ApprovedFor returns the approved, unexpired approval for a step, if any.
func ApprovedFor(ctx context.Context, q store.Querier, stepID string) (*Approval, error) {
	ap, err := scanApproval(q.QueryRowContext(ctx, "select "+apCols+
		" from approvals where step_id=? and status='approved' order by created_at desc limit 1", stepID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ap, err
}

// Decide approves or rejects a pending approval. Approving requires the exact
// action hash the owner reviewed; expired approvals are marked expired.
func Decide(ctx context.Context, tx *sql.Tx, id string, approve bool, hash, by string) (*Approval, error) {
	ap, err := GetApproval(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if ap.Status != "pending" {
		return ap, fmt.Errorf("%w: %s", ErrNotPending, ap.Status)
	}
	now := ids.NowMs()
	if now >= ap.ExpiresAt {
		if _, err := tx.ExecContext(ctx, "update approvals set status='expired' where id=?", id); err != nil {
			return nil, err
		}
		ap.Status = "expired"
		return ap, store.Commit(ErrExpired)
	}
	status := "rejected"
	if approve {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(ap.ActionHash)) != 1 || ap.Action().Hash() != ap.ActionHash {
			return ap, ErrMismatch
		}
		status = "approved"
	}
	if _, err := tx.ExecContext(ctx, "update approvals set status=?, decided_by=?, decided_at=? where id=?", status, by, now, id); err != nil {
		return nil, err
	}
	ap.Status = status
	return ap, nil
}

// Consume marks an approved approval used. It verifies the action about to be
// executed hashes to the approved hash and the approval has not expired; an
// approval can be consumed once. Expiry and mismatch are recorded durably
// (store.Commit) before the error is returned.
func Consume(ctx context.Context, tx *sql.Tx, id string, a Action) error {
	ap, err := GetApproval(ctx, tx, id)
	if err != nil {
		return err
	}
	if ap.Status != "approved" {
		return fmt.Errorf("%w: %s", ErrNotPending, ap.Status)
	}
	if ids.NowMs() >= ap.ExpiresAt {
		if _, err := tx.ExecContext(ctx, "update approvals set status='expired' where id=?", id); err != nil {
			return err
		}
		return store.Commit(ErrExpired)
	}
	if a.Hash() != ap.ActionHash {
		if _, err := tx.ExecContext(ctx, "update approvals set status='invalidated' where id=?", id); err != nil {
			return err
		}
		return store.Commit(ErrMismatch)
	}
	res, err := tx.ExecContext(ctx, "update approvals set status='consumed' where id=? and status='approved'", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotPending
	}
	return nil
}

// Audit appends a security-relevant decision to audit_events.
func Audit(ctx context.Context, tx *sql.Tx, typ, actor, taskID, requestID string, data map[string]any) error {
	b, _ := json.Marshal(data)
	var tid, rid any
	if taskID != "" {
		tid = taskID
	}
	if requestID != "" {
		rid = requestID
	}
	_, err := tx.ExecContext(ctx, "insert into audit_events(id, type, actor, task_id, request_id, data_json, at) values(?,?,?,?,?,?,?)",
		ids.New(), typ, actor, tid, rid, string(b), ids.NowMs())
	return err
}
