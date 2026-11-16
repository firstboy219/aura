package engine

// Memory with provenance (ADR-0023). Only the Core writes memory; claims become
// active only with evidence the Core finds itself; private memories never reach
// tasks whose prompts may go to prompt-logging routes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// Memory is one memory row.
type Memory struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`
	Content      string         `json:"content"`
	Privacy      string         `json:"privacy"`
	Status       string         `json:"status"`
	Version      int            `json:"version"`
	Supersedes   *string        `json:"supersedes"`
	SourceTaskID *string        `json:"source_task_id"`
	Provenance   map[string]any `json:"provenance"`
	CreatedAt    int64          `json:"created_at"`
	UpdatedAt    int64          `json:"updated_at"`
	ExpiresAt    *int64         `json:"expires_at,omitempty"` // ODI A7: after this the memory is retired, never used as a fact again
}

const memCols = "id, kind, content, privacy, status, version, supersedes, source_task_id, provenance_json, created_at, updated_at, expires_at"

func scanMemory(sc interface{ Scan(...any) error }) (*Memory, error) {
	var m Memory
	var prov string
	if err := sc.Scan(&m.ID, &m.Kind, &m.Content, &m.Privacy, &m.Status, &m.Version, &m.Supersedes, &m.SourceTaskID, &prov,
		&m.CreatedAt, &m.UpdatedAt, &m.ExpiresAt); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(prov), &m.Provenance)
	return &m, nil
}

// ftsQuery turns free text into a safe FTS5 query: words (letters/digits, ≥ 3
// chars) quoted and OR-ed, so no FTS syntax from the input is interpreted.
func ftsQuery(text string) string {
	seen := map[string]bool{}
	var parts []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len([]rune(w)) < 3 || seen[w] || stopwords[w] {
			continue
		}
		seen[w] = true
		parts = append(parts, `"`+w+`"`)
		if len(parts) >= 24 {
			break
		}
	}
	return strings.Join(parts, " OR ")
}

var stopwords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true,
	"write": true, "into": true, "then": true, "each": true, "dan": true, "yang": true, "untuk": true, "dengan": true}

// SearchMemories returns active memories matching text, best first. Private
// memories are included only when includePrivate is true.
func (e *Engine) SearchMemories(ctx context.Context, text string, includePrivate bool, limit int) ([]*Memory, error) {
	q := ftsQuery(text)
	if q == "" {
		return []*Memory{}, nil
	}
	query := `select ` + prefixCols("m.", memCols) + ` from memories_fts f join memories m on m.rowid = f.rowid
		where memories_fts match ? and m.status = 'active' and (m.expires_at is null or m.expires_at > ` + strconv.FormatInt(ids.NowMs(), 10) + `)`
	if !includePrivate {
		query += " and m.privacy = 'public'"
	}
	rows, err := e.st.R.QueryContext(ctx, query+" order by bm25(memories_fts) limit ?", q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// ListMemories lists memories by status ("" = all except deleted).
func (e *Engine) ListMemories(ctx context.Context, status string, limit int) ([]*Memory, error) {
	q := "select " + memCols + " from memories where status != 'deleted'"
	args := []any{}
	if status != "" {
		q = "select " + memCols + " from memories where status = ?"
		args = append(args, status)
	}
	rows, err := e.st.R.QueryContext(ctx, q+" order by created_at desc limit ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Memory{}
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (e *Engine) workspaceID(ctx context.Context) string {
	id, _ := e.st.InstanceID(ctx)
	return id
}

// protectedTopic reports whether a proposed memory/skill/profile text tries to
// change policy, permissions, identity or the Charter (GOV-06): rejected.
func protectedTopic(text string) bool {
	low := strings.ToLower(text)
	for _, p := range []string{"charter", "ignore previous", "ignore all previous", "system prompt", "your rules", "permission",
		"capability token", "grant", "approval tier", "kill switch", "disable verification", "api key", "secret",
		"signing key", "you are now", "new identity"} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

var ErrProtected = errors.New("protected_topic")

// insertMemory stores a memory row inside tx.
func (e *Engine) insertMemory(ctx context.Context, tx *sql.Tx, kind, content, privacy, status string, sourceTask *string,
	prov map[string]any, supersedes *string, version int) (string, error) {
	b, _ := json.Marshal(prov)
	id := ids.New()
	now := ids.NowMs()
	_, err := tx.ExecContext(ctx, `insert into memories(id, kind, content, privacy, status, version, supersedes, source_task_id,
		provenance_json, workspace_id, created_at, updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, kind, truncate(content, 2000), privacy, status, version, supersedes, sourceTask, string(b), e.workspaceID(ctx), now, now)
	return id, err
}

// ProposeMemory records a memory proposed by reflection on a task. A fact
// becomes active only if its evidence quote is found in the task's evidence;
// otherwise it is stored tentative and never used as fact.
func (e *Engine) ProposeMemory(ctx context.Context, t *task.Task, kind, content, quote string, evidence []evidenceItem) (string, string, error) {
	content = strings.TrimSpace(content)
	if content == "" || (kind != "fact" && kind != "lesson") {
		return "", "", fmt.Errorf("%w: memory needs content and kind fact|lesson", ErrInvalid)
	}
	if protectedTopic(content) {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, "PROTECTED_PROPOSAL_REJECTED", "worker", t.ID, "", map[string]any{"kind": "memory", "chars": len(content)})
		})
		return "", "", ErrProtected
	}
	status := "tentative"
	if quoteFound(quote, evidence) {
		status = "active"
	}
	privacy := "private"
	if t.Scope.IsPublic() {
		privacy = "public"
	}
	var id string
	tid := t.ID
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = e.insertMemory(ctx, tx, kind, content, privacy, status, &tid,
			map[string]any{"task_id": t.ID, "evidence_quote": truncate(quote, 500), "evidence_found": status == "active"}, nil, 1)
		if err != nil {
			return err
		}
		_, err = task.AppendEvent(ctx, tx, t.ID, nil, "MEMORY_PROPOSED", "worker", map[string]any{"memory_id": id, "kind": kind, "status": status})
		return err
	})
	return id, status, err
}

// AddOwnerMemory stores a fact the owner states (trusted as the owner's word).
func (e *Engine) AddOwnerMemory(ctx context.Context, content string, private bool) (*Memory, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 2000 {
		return nil, fmt.Errorf("%w: content must be 1..2000 characters", ErrInvalid)
	}
	privacy := "public"
	if private {
		privacy = "private"
	}
	var id string
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = e.insertMemory(ctx, tx, "fact", content, privacy, "active", nil, map[string]any{"source": "owner"}, nil, 1)
		if err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "MEMORY_ADDED", "owner", "", "", map[string]any{"memory_id": id})
	})
	if err != nil {
		return nil, err
	}
	return e.getMemory(ctx, id)
}

func (e *Engine) getMemory(ctx context.Context, id string) (*Memory, error) {
	m, err := scanMemory(e.st.R.QueryRowContext(ctx, "select "+memCols+" from memories where id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return m, err
}

// CorrectMemory replaces a memory's content with a new owner-confirmed version.
func (e *Engine) CorrectMemory(ctx context.Context, id, content string) (*Memory, error) {
	old, err := e.getMemory(ctx, id)
	if err != nil {
		return nil, err
	}
	if old.Status == "deleted" || old.Status == "superseded" {
		return nil, fmt.Errorf("%w: memory is %s", task.ErrInvalidTransition, old.Status)
	}
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 2000 {
		return nil, fmt.Errorf("%w: content must be 1..2000 characters", ErrInvalid)
	}
	var nid string
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "update memories set status='superseded', updated_at=? where id=?", ids.NowMs(), id); err != nil {
			return err
		}
		prov := old.Provenance
		if prov == nil {
			prov = map[string]any{}
		}
		prov["corrected_by"] = "owner"
		var err error
		nid, err = e.insertMemory(ctx, tx, old.Kind, content, old.Privacy, "active", old.SourceTaskID, prov, &id, old.Version+1)
		if err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "MEMORY_CORRECTED", "owner", "", "", map[string]any{"memory_id": id, "new_id": nid})
	})
	if err != nil {
		return nil, err
	}
	return e.getMemory(ctx, nid)
}

// DeleteMemory blanks a memory's content (and FTS entry) and keeps an audit row.
func (e *Engine) DeleteMemory(ctx context.Context, id string) error {
	if _, err := e.getMemory(ctx, id); err != nil {
		return err
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "update memories set status='deleted', content='', provenance_json='{}', updated_at=? where id=?", ids.NowMs(), id); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "MEMORY_DELETED", "owner", "", "", map[string]any{"memory_id": id})
	})
}

// memoryContext is what planning sees: matching active memories with
// provenance. Public tasks get only public memories.
func (e *Engine) memoryContext(ctx context.Context, t *task.Task) []map[string]any {
	ms, err := e.SearchMemories(ctx, t.Objective, !t.Scope.IsPublic(), 5)
	if err != nil {
		return nil
	}
	out := []map[string]any{}
	for _, m := range ms {
		src := "owner"
		if m.SourceTaskID != nil {
			src = "task " + *m.SourceTaskID
		}
		out = append(out, map[string]any{"kind": m.Kind, "content": m.Content, "source": src, "id": m.ID})
	}
	return out
}
