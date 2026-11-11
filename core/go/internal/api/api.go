// Package api is the Phase 1 REST API (Blueprint §11, contracts/api/openapi.yaml):
// bearer-token auth, loopback by default, request IDs, structured errors,
// body limits, rate limiting and audit. There is no shell or execution endpoint.
package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/engine"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
	"aura.local/core/internal/vault"
	"aura.local/core/internal/worker"
)

const maxBody = 64 << 10

// Readiness reports named checks; all must be "ok" for /system/health to be ready.
type Readiness func(ctx context.Context) map[string]string

// Server serves the API.
type Server struct {
	Engine     *engine.Engine
	Store      *store.Store
	Governor   *budget.Governor
	Worker     *worker.Supervisor
	Token      string
	Version    string
	Log        *slog.Logger
	Ready      Readiness
	RatePerSec int
	// AcceptTasks returns an error code (e.g. "disk_pressure") when new tasks must be refused.
	AcceptTasks func() string
	// Webhooks are the configured inbound webhooks by name (ADR-0034).
	Webhooks map[string]Webhook
	// Web is the Workspace Web App configuration (ADR-0041); Vault holds its TOTP secret.
	Web      WebConfig
	Vault    *vault.Vault
	loginLim loginLimiter

	once sync.Once
	lim  *limiter
}

type ctxKey int

const reqIDKey ctxKey = 0

// Handler builds the HTTP handler.
func (s *Server) Handler() http.Handler {
	s.once.Do(func() {
		r := s.RatePerSec
		if r <= 0 {
			r = 20
		}
		s.lim = newLimiter(float64(r), float64(2*r))
		if s.Log == nil {
			s.Log = slog.Default()
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/system/health", s.health)
	auth := func(h http.HandlerFunc) http.Handler { return s.authed(h) }
	mux.Handle("POST /api/v1/tasks", auth(s.createTask))
	mux.Handle("GET /api/v1/tasks", auth(s.listTasks))
	mux.Handle("GET /api/v1/tasks/{id}", auth(s.getTask))
	mux.Handle("GET /api/v1/tasks/{id}/events", auth(s.taskEvents))
	mux.Handle("POST /api/v1/tasks/{id}/input", auth(s.answer))
	mux.Handle("POST /api/v1/tasks/{id}/priority", auth(s.priority))
	mux.Handle("POST /api/v1/events", auth(s.event))
	mux.HandleFunc("POST /api/v1/webhooks/{name}", s.webhook) // HMAC-signed, no bearer token
	mux.Handle("GET /api/v1/tasks/{id}/tree", auth(s.tree))
	mux.Handle("GET /api/v1/tasks/{id}/timeline", auth(s.timeline))
	mux.Handle("GET /api/v1/tasks/{id}/files", auth(s.files))
	mux.Handle("GET /api/v1/tasks/{id}/files/{path...}", auth(s.file))
	mux.Handle("GET /api/v1/stream", auth(s.stream))
	mux.Handle("GET /api/v1/memories", auth(s.listMemories))
	mux.Handle("POST /api/v1/memories", auth(s.addMemory))
	mux.Handle("POST /api/v1/memories/{id}/correct", auth(s.correctMemory))
	mux.Handle("DELETE /api/v1/memories/{id}", auth(s.deleteMemory))
	mux.Handle("GET /api/v1/skills", auth(s.listSkills))
	mux.Handle("GET /api/v1/skills/{id}", auth(s.getSkill))
	mux.Handle("POST /api/v1/skills/import", auth(s.importSkill))
	mux.Handle("POST /api/v1/skills/{id}/{action}", auth(s.skillAction))
	mux.HandleFunc("POST /api/v1/web/login", s.webOnly(s.webLogin))
	mux.HandleFunc("POST /api/v1/web/logout", s.webOnly(s.webLogout))
	mux.HandleFunc("GET /api/v1/web/session", s.webOnly(s.webSessionInfo))
	mux.HandleFunc("POST /api/v1/web/setup/start", s.webOnly(s.webSetupStart))
	mux.HandleFunc("POST /api/v1/web/setup/confirm", s.webOnly(s.webSetupConfirm))
	mux.HandleFunc("GET /app/", s.webOnly(s.webApp))
	mux.Handle("POST /api/v1/browser/check", auth(s.browserCheck))
	mux.Handle("GET /api/v1/browser/profiles", auth(s.browserProfiles))
	mux.Handle("POST /api/v1/browser/profiles/{name}/session", auth(s.importBrowserSession))
	mux.Handle("DELETE /api/v1/browser/profiles/{name}/session", auth(s.deleteBrowserSession))
	mux.Handle("POST /api/v1/tasks/{id}/browser-code", auth(s.browserCode))
	mux.Handle("POST /api/v1/tasks/{id}/browser/takeover", auth(s.browserTakeover))
	mux.Handle("GET /api/v1/browser/sessions", auth(s.browserSessions))
	mux.Handle("GET /api/v1/browser/sessions/{key}/screen", auth(s.browserScreen))
	mux.Handle("POST /api/v1/browser/sessions/{key}/input", auth(s.browserInput))
	mux.Handle("POST /api/v1/browser/sessions/{key}/return", auth(s.browserReturn))
	mux.Handle("POST /api/v1/browser/profiles/{name}/login", auth(s.profileLoginStart))
	mux.Handle("POST /api/v1/browser/profiles/{name}/login/finish", auth(s.profileLoginFinish))
	mux.Handle("GET /api/v1/tools", auth(s.listL2))
	mux.Handle("GET /api/v1/tools/{id}", auth(s.getL2))
	mux.Handle("POST /api/v1/tools/{id}/{action}", auth(s.l2Action))
	mux.Handle("GET /api/v1/brain/variants", auth(s.listBrain))
	mux.Handle("POST /api/v1/aul/check", auth(s.aulCheck))
	mux.Handle("GET /api/v1/workflows", auth(s.listWorkflows))
	mux.Handle("POST /api/v1/workflows", auth(s.registerWorkflow))
	mux.Handle("GET /api/v1/workflows/{id}", auth(s.getWorkflow))
	mux.Handle("POST /api/v1/workflows/{id}/run", auth(s.runWorkflow))
	mux.Handle("POST /api/v1/workflows/{id}/disable", auth(s.disableWorkflow))
	mux.Handle("GET /api/v1/brain/variants/{id}", auth(s.getBrain))
	mux.Handle("POST /api/v1/brain/variants/{id}/{action}", auth(s.brainAction))
	mux.Handle("GET /api/v1/self-model", auth(s.selfModel))
	mux.Handle("GET /api/v1/self", auth(s.self))
	mux.Handle("GET /api/v1/triggers", auth(s.listTriggers))
	mux.Handle("POST /api/v1/triggers", auth(s.createTrigger))
	mux.Handle("POST /api/v1/triggers/{id}/{action}", auth(s.triggerAction))
	mux.Handle("GET /api/v1/ui/home", auth(s.uiHome))
	mux.Handle("GET /api/v1/ui/screens/{name}", auth(s.uiScreen))
	mux.Handle("POST /api/v1/ui/screens/{name}/{action}", auth(s.uiScreenAction))
	mux.Handle("GET /api/v1/notes", auth(s.listNotes))
	mux.Handle("POST /api/v1/notes", auth(s.addNote))
	mux.Handle("PATCH /api/v1/notes/{id}", auth(s.updateNote))
	mux.Handle("DELETE /api/v1/notes/{id}", auth(s.deleteNote))
	mux.Handle("GET /api/v1/reminders", auth(s.listReminders))
	mux.Handle("DELETE /api/v1/reminders/{id}", auth(s.deleteReminder))
	mux.Handle("GET /api/v1/media/{id}", auth(s.media))
	mux.Handle("GET /api/v1/notifications", auth(s.notifications))
	mux.Handle("POST /api/v1/notifications/{id}/read", auth(s.markRead))
	mux.Handle("GET /api/v1/limitations", auth(s.listLimitations))
	mux.Handle("POST /api/v1/limitations", auth(s.addLimitation))
	mux.Handle("POST /api/v1/limitations/{id}/{action}", auth(s.limitationAction))
	mux.Handle("POST /api/v1/improvement/run", auth(s.improvementRun))
	mux.Handle("GET /api/v1/scorecard", auth(s.scorecard))
	mux.Handle("GET /api/v1/routing/shadow", auth(s.shadow))
	mux.Handle("GET /api/v1/profile", auth(s.listProfile))
	mux.Handle("POST /api/v1/profile", auth(s.addProfile))
	mux.Handle("POST /api/v1/profile/{id}/{action}", auth(s.profileAction))
	mux.Handle("POST /api/v1/tasks/{id}/context", auth(s.addContext))
	mux.Handle("POST /api/v1/tasks/{id}/{action}", auth(s.taskAction))
	mux.HandleFunc("POST /api/v1/app/login", s.appLogin) // ADR-0053: unauthenticated, rate limited, account lockout
	mux.Handle("POST /api/v1/app/logout", auth(s.appLogout))
	mux.Handle("GET /api/v1/admin/app-accounts", auth(s.listAppAccounts))
	mux.Handle("PUT /api/v1/admin/app-accounts/{username}", auth(s.putAppAccount))
	mux.Handle("POST /api/v1/admin/app-accounts/{username}/disable", auth(s.disableAppAccount))
	mux.Handle("GET /api/v1/admin/members", auth(s.listMembers))
	mux.Handle("POST /api/v1/admin/members", auth(s.createMember))
	mux.Handle("POST /api/v1/admin/members/{id}/disable", auth(s.disableMember))
	mux.Handle("POST /api/v1/admin/members/{id}/department", auth(s.setDepartment))
	mux.Handle("GET /api/v1/admin/audit", auth(s.auditSearch))
	mux.Handle("GET /api/v1/admin/usage", auth(s.usageReport))
	mux.Handle("GET /api/v1/admin/budget", auth(s.getBudget))
	mux.Handle("PUT /api/v1/admin/budget", auth(s.putBudget))
	mux.Handle("GET /api/v1/admin/credentials", auth(s.listCredentials))
	mux.Handle("PUT /api/v1/admin/credentials/{name}", auth(s.putCredential))
	mux.Handle("DELETE /api/v1/admin/credentials/{name}", auth(s.deleteCredential))
	mux.Handle("GET /api/v1/approvals", auth(s.listApprovals))
	mux.Handle("POST /api/v1/approvals/{id}/{decision}", auth(s.decide))
	mux.Handle("GET /api/v1/system/resources", auth(s.resources))
	mux.Handle("POST /api/v1/system/stop", auth(s.stop))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && s.isWebHost(r) {
			http.Redirect(w, r, "/app/", http.StatusFound)
			return
		}
		s.fail(w, r, 404, "not_found", "no such endpoint")
	})
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := ids.New()
		w.Header().Set("X-Request-Id", rid)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r = r.WithContext(context.WithValue(r.Context(), reqIDKey, rid))
		limit := int64(maxBody)
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/browser/profiles/") {
			limit = 1 << 20 // an exported browser session (cookies and storage)
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		start := time.Now()
		defer func() {
			if p := recover(); p != nil {
				s.Log.Error("panic in handler", "request_id", rid, "panic", fmt.Sprint(p))
				s.fail(w, r, 500, "internal", "internal error")
			}
			s.Log.Info("http", "request_id", rid, "method", r.Method, "path", r.URL.Path, "ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(h, "Bearer ")
		bearer := ok && s.Token != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) == 1
		principal := Principal{ID: "owner", Name: "owner", Role: RoleOwner}
		if !bearer && ok {
			if m, found := s.memberByToken(r, tok); found {
				bearer, principal = true, m
			}
		}
		if !bearer {
			// The Web App's session cookie counts only on the web origin, with a CSRF
			// token and a matching Origin on every state-changing request (ADR-0041).
			if why := s.webSession(w, r); why != "" {
				s.audit(r, "AUTH_FAILED", map[string]any{"path": r.URL.Path, "remote": s.clientIP(r), "why": why})
				s.fail(w, r, 401, "unauthorized", "missing or invalid credentials")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), webKey, true))
		}
		if !allowed(principal.Role, r.Pattern) {
			s.audit(r, "AUTHZ_DENIED", map[string]any{"route": r.Pattern, "member": principal.Name, "role": principal.Role})
			s.fail(w, r, 403, "forbidden", "your role ("+principal.Role+") may not do this")
			return
		}
		r = withPrincipal(r, principal)
		if !s.lim.allow() {
			s.fail(w, r, 429, "rate_limited", "too many requests")
			return
		}
		next(w, r)
	})
}

func requestID(r *http.Request) string {
	v, _ := r.Context().Value(reqIDKey).(string)
	return v
}

func (s *Server) audit(r *http.Request, typ string, data map[string]any) {
	s.Store.Write(context.WithoutCancel(r.Context()), func(tx *sql.Tx) error {
		return policy.Audit(r.Context(), tx, typ, "owner", "", requestID(r), data)
	})
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg, RequestID: requestID(r)}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// mapErr turns domain errors into HTTP errors without leaking internals.
func (s *Server) mapErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, engine.ErrInvalid):
		s.fail(w, r, 400, "invalid_input", strings.TrimPrefix(err.Error(), "invalid_input: "))
	case errors.Is(err, task.ErrNotFound), errors.Is(err, policy.ErrApprovalMissing), errors.Is(err, engine.ErrNoBrowserSession):
		s.fail(w, r, 404, "not_found", "not found")
	case errors.Is(err, task.ErrInvalidTransition):
		s.fail(w, r, 409, "invalid_transition", err.Error())
	case errors.Is(err, policy.ErrMismatch):
		s.fail(w, r, 409, "approval_mismatch", "action_hash does not match the pending approval")
	case errors.Is(err, policy.ErrExpired):
		s.fail(w, r, 410, "approval_expired", "approval expired")
	case errors.Is(err, engine.ErrProtected):
		s.fail(w, r, 422, "protected_topic", "content that targets policy, permissions, identity or the Charter is not accepted")
	case errors.Is(err, policy.ErrNotPending):
		s.fail(w, r, 409, "conflict", err.Error())
	default:
		s.Log.Error("request failed", "request_id", requestID(r), "err", err)
		s.fail(w, r, 500, "internal", "internal error")
	}
}

func decodeStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", engine.ErrInvalid, err)
	}
	return nil
}

// Task is the API representation (openapi Task).
type Task struct {
	ID           string      `json:"id"`
	State        task.State  `json:"state"`
	Reason       *string     `json:"reason"`
	ReasonCode   *string     `json:"reason_code"`
	TaskClass    *string     `json:"task_class"`
	WaitReason   *string     `json:"wait_reason"`
	Objective    string      `json:"objective"`
	ParentTaskID *string     `json:"parent_task_id"`
	RootTaskID   string      `json:"root_task_id"`
	AgentRole    string      `json:"agent_role"`
	Depth        int         `json:"depth"`
	CostUSD      float64     `json:"cost_usd"`
	Budget       task.Budget `json:"budget"`
	Tools        []string    `json:"tools"`
	AllowDomains []string    `json:"allow_domains"`
	DataClass    string      `json:"data_class"`
	Recovery     string      `json:"recovery"`
	Attachments  []string    `json:"attachments,omitempty"`
	Priority     int         `json:"priority"`
	Replans      int         `json:"replans"`
	Tier         *string     `json:"tier"`
	Question     *string     `json:"question,omitempty"`
	// HeardAs is what AURA understood a spoken objective to mean (ADR-0058).
	HeardAs   string        `json:"heard_as,omitempty"`
	Reply     *engine.Reply `json:"reply,omitempty"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

func recoveryOf(t *task.Task) string {
	if t.Scope.Recovery == "" {
		return "auto"
	}
	return t.Scope.Recovery
}

func ts(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func toAPI(t *task.Task) Task {
	return Task{ID: t.ID, State: t.State, Reason: t.Reason, ReasonCode: t.ReasonCode, TaskClass: t.TaskClass, WaitReason: t.WaitReason, Objective: t.Objective,
		ParentTaskID: t.ParentTaskID, RootTaskID: t.RootTaskID, AgentRole: t.AgentRole, Depth: t.Depth,
		CostUSD: t.CostUSD + t.CostReservedUSD, Budget: t.Budget, Tools: t.Scope.Tools, AllowDomains: t.Scope.AllowDomains, DataClass: t.Scope.DataClass, Recovery: recoveryOf(t), Attachments: t.Scope.Attachments,
		Priority: t.Priority, Replans: t.Replans, Tier: t.TierOverride,
		CreatedAt: ts(t.CreatedAt), UpdatedAt: ts(t.UpdatedAt)}
}

type createReq struct {
	Objective    string            `json:"objective"`
	Tools        []string          `json:"tools"`
	AllowDomains []string          `json:"allow_domains"`
	DataClass    string            `json:"data_class"`
	Tier         string            `json:"tier"`
	Recovery     string            `json:"recovery"`
	Reply        string            `json:"reply"`
	BrowserProf  string            `json:"browser_profile"`
	Attachments  map[string]string `json:"attachments"`
	Voice        *task.Voice       `json:"voice"`
	Design       string            `json:"design"`
	Budget       *struct {
		MaxCostUSD     float64 `json:"max_cost_usd"`
		MaxIterations  int     `json:"max_iterations"`
		MaxWallSeconds int     `json:"max_wall_seconds"`
	} `json:"budget"`
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	if s.AcceptTasks != nil {
		if code := s.AcceptTasks(); code != "" {
			s.fail(w, r, 503, code, "not accepting new tasks: "+code)
			return
		}
	}
	var in createReq
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	ci := engine.CreateInput{Objective: in.Objective, Tools: in.Tools, AllowDomains: in.AllowDomains, DataClass: in.DataClass, Tier: in.Tier, Recovery: in.Recovery, BrowserProfile: in.BrowserProf, Attachments: in.Attachments,
		Reply: in.Reply, Voice: in.Voice, Design: in.Design, CreatedBy: PrincipalOf(r).ID, IdempotencyKey: r.Header.Get("Idempotency-Key")}
	if in.Budget != nil {
		ci.Budget = task.Budget{MaxCostUSD: in.Budget.MaxCostUSD, MaxIterations: in.Budget.MaxIterations, MaxWallSeconds: in.Budget.MaxWallSeconds}
	}
	t, created, err := s.Engine.Submit(r.Context(), ci)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	} else {
		s.attribute(r, t.ID)
	}
	writeJSON(w, status, toAPI(t))
}

// attribute records the principal that created a task (usage reports, ADR-0049).
func (s *Server) attribute(r *http.Request, taskID string) {
	p := PrincipalOf(r)
	s.Store.Write(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), "update tasks set created_by=? where id=?", p.ID, taskID)
		return err
	})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			s.fail(w, r, 400, "invalid_input", "limit must be 1..200")
			return
		}
		limit = n
	}
	ts, err := task.List(r.Context(), s.Store.R, task.State(q.Get("state")), limit+1, q.Get("cursor"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	var next *string
	if len(ts) > limit {
		ts = ts[:limit]
		id := ts[limit-1].ID
		next = &id
	}
	ids := make([]string, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	replies, _ := s.Engine.Replies(r.Context(), ids)
	out := []Task{}
	for _, t := range ts {
		at := toAPI(t)
		at.Reply = replies[t.ID]
		out = append(out, at)
	}
	writeJSON(w, 200, map[string]any{"tasks": out, "next_cursor": next})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := task.Get(r.Context(), s.Store.R, r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	out := toAPI(t)
	if t.State == task.WaitingInput {
		out.Question = s.Engine.PendingQuestion(r.Context(), t.ID)
	}
	out.Reply, _ = s.Engine.GetReply(r.Context(), t.ID)
	if t.Scope.Voice != nil {
		out.HeardAs = s.Engine.HeardAs(r.Context(), t.ID)
	}
	writeJSON(w, 200, out)
}

func (s *Server) priority(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Priority int `json:"priority"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	t, err := s.Engine.SetPriority(r.Context(), r.PathValue("id"), in.Priority)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, toAPI(t))
}

func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key     string          `json:"key"`
		DedupID string          `json:"dedup_id"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	id, dup, err := s.Engine.ExternalEvent(r.Context(), in.Key, in.DedupID, in.Payload)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	status := http.StatusAccepted
	if dup {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"id": id, "duplicate": dup})
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	n, err := s.Engine.Tree(r.Context(), r.PathValue("id"))
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, treeOut(n))
}

type treeNode struct {
	Task        Task       `json:"task"`
	TreeCostUSD float64    `json:"tree_cost_usd"`
	Children    []treeNode `json:"children"`
}

func treeOut(n *engine.TreeNode) treeNode {
	out := treeNode{Task: toAPI(n.Task), TreeCostUSD: n.TreeCostUSD, Children: []treeNode{}}
	for _, c := range n.Children {
		out.Children = append(out.Children, treeOut(c))
	}
	return out
}

func (s *Server) addContext(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Content string `json:"content"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	t, err := s.Engine.AddContext(r.Context(), r.PathValue("id"), in.Content)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, toAPI(t))
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Answer string `json:"answer"`
	}
	if err := decodeStrict(r, &in); err != nil {
		s.mapErr(w, r, err)
		return
	}
	t, err := s.Engine.Answer(r.Context(), r.PathValue("id"), in.Answer)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, toAPI(t))
}

type eventOut struct {
	Seq    int64           `json:"seq"`
	Type   string          `json:"type"`
	StepID *string         `json:"step_id"`
	Actor  string          `json:"actor"`
	At     string          `json:"at"`
	Data   json.RawMessage `json:"data"`
}

func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := task.Get(r.Context(), s.Store.R, id); err != nil {
		s.mapErr(w, r, err)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	evs, err := task.Events(r.Context(), s.Store.R, id, after, true)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	out := []eventOut{}
	for _, e := range evs {
		out = append(out, eventOut{Seq: e.Seq, Type: e.Type, StepID: e.StepID, Actor: e.Actor, At: ts(e.At), Data: e.Data})
	}
	writeJSON(w, 200, map[string]any{"events": out})
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var t *task.Task
	var err error
	switch r.PathValue("action") {
	case "cancel":
		t, err = s.Engine.Cancel(r.Context(), id)
	case "pause":
		t, err = s.Engine.Pause(r.Context(), id)
	case "resume":
		t, err = s.Engine.Resume(r.Context(), id)
	default:
		s.fail(w, r, 404, "not_found", "no such action")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, toAPI(t))
}

type approvalOut struct {
	*policy.Approval
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
	// Blueprint 13A.7 rule 4: everything the owner needs to decide.
	Tier          string  `json:"tier,omitempty"`
	TaskObjective string  `json:"task_objective,omitempty"`
	StepNo        int     `json:"step_no,omitempty"`
	Why           string  `json:"why,omitempty"` // the step's description from the plan
	EstimatedCost string  `json:"estimated_cost,omitempty"`
	TaskCostUSD   float64 `json:"task_cost_usd"`
}

func apOut(ap *policy.Approval) approvalOut {
	return approvalOut{Approval: ap, ExpiresAt: ts(ap.ExpiresAt), CreatedAt: ts(ap.CreatedAt)}
}

// apFull adds the task, step, tier and cost context to an approval.
func (s *Server) apFull(ctx context.Context, ap *policy.Approval) approvalOut {
	o := apOut(ap)
	if spec, ok := policy.Lookup(ap.ToolID); ok {
		o.Tier = string(spec.Tier(ap.Parameters))
	}
	if t, err := task.Get(ctx, s.Store.R, ap.TaskID); err == nil {
		o.TaskObjective, o.TaskCostUSD = t.Objective, t.CostUSD+t.CostReservedUSD
	}
	if st, err := task.GetStep(ctx, s.Store.R, ap.StepID); err == nil {
		o.StepNo, o.Why = st.StepNo, st.Description
	}
	o.EstimatedCost = "no model cost for the action itself; it runs a tool"
	return o
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	aps, err := policy.Pending(r.Context(), s.Store.R)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	out := []approvalOut{}
	for _, ap := range aps {
		out = append(out, s.apFull(r.Context(), ap))
	}
	writeJSON(w, 200, map[string]any{"approvals": out})
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ap *policy.Approval
	var err error
	switch r.PathValue("decision") {
	case "approve":
		var in struct {
			ActionHash string `json:"action_hash"`
			Code       string `json:"code"` // web: step-up TOTP code (Blueprint 13A.7 rule 4)
		}
		if err := decodeStrict(r, &in); err != nil || len(in.ActionHash) != 64 {
			s.fail(w, r, 400, "invalid_input", "body must be {\"action_hash\": \"<64 hex>\"}")
			return
		}
		by := "owner"
		if p := PrincipalOf(r); p.Role != RoleOwner {
			by = "member:" + p.Name + " (" + p.Role + ")"
		}
		if viaWeb(r) {
			if !s.stepUp(r, in.Code) {
				s.audit(r, "WEB_STEP_UP_FAILED", map[string]any{"approval_id": id, "remote": s.clientIP(r)})
				s.fail(w, r, 401, "step_up_required", "enter a current code from your authenticator app to approve")
				return
			}
			by = "owner(web, step-up)"
		}
		ap, err = s.Engine.Approve(r.Context(), id, in.ActionHash, by)
	case "reject":
		by := "owner"
		if p := PrincipalOf(r); p.Role != RoleOwner {
			by = "member:" + p.Name + " (" + p.Role + ")"
		}
		if viaWeb(r) {
			by = "owner(web)"
		}
		ap, err = s.Engine.Reject(r.Context(), id, by) // rejecting never needs step-up
	default:
		s.fail(w, r, 404, "not_found", "no such decision")
		return
	}
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, 200, apOut(ap))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	if s.Ready != nil {
		checks = s.Ready(r.Context())
	}
	ready := true
	for _, v := range checks {
		if v != "ok" {
			ready = false
		}
	}
	status := 200
	if !ready {
		status = 503
	}
	writeJSON(w, status, map[string]any{"live": true, "ready": ready, "version": s.Version, "checks": checks})
}

func (s *Server) resources(w http.ResponseWriter, r *http.Request) {
	state, pid, gen := s.Worker.State()
	reserved, spent, _ := s.Governor.Today(r.Context())
	var pidv any
	if pid != 0 {
		pidv = pid
	}
	writeJSON(w, 200, map[string]any{
		"core_rss_bytes": worker.ProcRSS(os.Getpid()),
		"worker":         map[string]any{"state": state, "pid": pidv, "generation": gen, "rss_bytes": s.Worker.RSS()},
		"db":             map[string]any{"wal_bytes": s.Store.WALBytes(), "write_queue_depth": s.Store.QueueDepth(), "sqlite_version": s.Store.SQLiteVersion()},
		"budget_today":   map[string]any{"reserved_usd": reserved, "spent_usd": spent, "limit_usd": s.Governor.Limit()},
		"halted":         s.Governor.Halted(),
		"charter":        s.Engine.CharterStatus(),
	})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	n, err := s.Engine.Stop(r.Context(), "owner")
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"stopped_tasks": n})
}

// limiter is a token bucket.
type limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
