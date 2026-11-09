package engine

// Owner Working Profile (ADR-0026): explicit preferences are confirmed;
// inferred ones stay tentative (labelled hints, never rules) until the owner
// confirms. Versioned, inspectable, correctable, scoped to this workspace.
// Profile items are personal, so they are never sent with public tasks (which
// may use prompt-logging routes, A-20).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

type ProfileItem struct {
	ID           string  `json:"id"`
	Statement    string  `json:"statement"`
	Scope        string  `json:"scope"`
	Provenance   string  `json:"provenance"`
	SourceTaskID *string `json:"source_task_id"`
	Confidence   float64 `json:"confidence"`
	Status       string  `json:"status"`
	Version      int     `json:"version"`
	Supersedes   *string `json:"supersedes"`
	UpdatedAt    int64   `json:"updated_at"`
}

const profCols = "id, statement, scope, provenance, source_task_id, confidence, status, version, supersedes, updated_at"

func scanProfile(sc interface{ Scan(...any) error }) (*ProfileItem, error) {
	var p ProfileItem
	err := sc.Scan(&p.ID, &p.Statement, &p.Scope, &p.Provenance, &p.SourceTaskID, &p.Confidence, &p.Status, &p.Version, &p.Supersedes, &p.UpdatedAt)
	return &p, err
}

func (e *Engine) insertProfile(ctx context.Context, tx *sql.Tx, p *ProfileItem) error {
	p.ID = ids.New()
	now := ids.NowMs()
	p.UpdatedAt = now
	_, err := tx.ExecContext(ctx, `insert into profile_items(id, statement, scope, provenance, source_task_id, confidence, status, version,
		supersedes, workspace_id, created_at, updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Statement, p.Scope, p.Provenance, p.SourceTaskID, p.Confidence, p.Status, p.Version, p.Supersedes, e.workspaceID(ctx), now, now)
	return err
}

func cleanStatement(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 500 {
		return "", fmt.Errorf("%w: statement must be 1..500 characters", ErrInvalid)
	}
	if protectedTopic(s) {
		return "", ErrProtected
	}
	return s, nil
}

// AddPreference records an explicit owner preference (confirmed).
func (e *Engine) AddPreference(ctx context.Context, statement, scope string) (*ProfileItem, error) {
	s, err := cleanStatement(statement)
	if err != nil {
		return nil, err
	}
	if scope == "" {
		scope = "global"
	}
	p := &ProfileItem{Statement: s, Scope: truncate(scope, 60), Provenance: "explicit", Confidence: 1, Status: "confirmed", Version: 1}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		if err := e.insertProfile(ctx, tx, p); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "PROFILE_ADDED", "owner", "", "", map[string]any{"id": p.ID})
	})
	return p, err
}

// inferPreference records a preference inferred from an owner correction (tentative).
func (e *Engine) inferPreference(ctx context.Context, t *task.Task, statement string) (*ProfileItem, error) {
	s, err := cleanStatement(statement)
	if err != nil {
		return nil, err
	}
	var dup int
	e.st.R.QueryRowContext(ctx, "select count(*) from profile_items where statement=? and status in ('confirmed','tentative')", s).Scan(&dup)
	if dup > 0 {
		return nil, errors.New("duplicate preference")
	}
	tid := t.ID
	p := &ProfileItem{Statement: s, Scope: "global", Provenance: "inferred", SourceTaskID: &tid, Confidence: 0.5, Status: "tentative", Version: 1}
	err = e.st.Write(ctx, func(tx *sql.Tx) error { return e.insertProfile(ctx, tx, p) })
	return p, err
}

// ListProfile lists profile items (deleted and superseded only when all is true).
func (e *Engine) ListProfile(ctx context.Context, all bool) ([]*ProfileItem, error) {
	q := "select " + profCols + " from profile_items"
	if !all {
		q += " where status in ('confirmed','tentative','disabled')"
	}
	rows, err := e.st.R.QueryContext(ctx, q+" order by updated_at desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ProfileItem{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (e *Engine) getProfile(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*ProfileItem, error) {
	p, err := scanProfile(q.QueryRowContext(ctx, "select "+profCols+" from profile_items where id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return p, err
}

// UpdateProfile applies an owner action: confirm, disable, enable, delete, or
// edit (new version, confirmed).
func (e *Engine) UpdateProfile(ctx context.Context, id, action, statement string) (*ProfileItem, error) {
	var out *ProfileItem
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		p, err := e.getProfile(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Status == "deleted" || p.Status == "superseded" {
			return fmt.Errorf("%w: item is %s", task.ErrInvalidTransition, p.Status)
		}
		set := func(status string) error {
			_, err := tx.ExecContext(ctx, "update profile_items set status=?, updated_at=? where id=?", status, ids.NowMs(), id)
			p.Status = status
			return err
		}
		switch action {
		case "confirm", "enable":
			err = set("confirmed")
		case "disable":
			err = set("disabled")
		case "delete":
			if _, err = tx.ExecContext(ctx, "update profile_items set status='deleted', statement='', updated_at=? where id=?", ids.NowMs(), id); err == nil {
				p.Status, p.Statement = "deleted", ""
			}
		case "edit":
			s, cerr := cleanStatement(statement)
			if cerr != nil {
				return cerr
			}
			if err = set("superseded"); err != nil {
				return err
			}
			sup := id
			np := &ProfileItem{Statement: s, Scope: p.Scope, Provenance: "explicit", SourceTaskID: p.SourceTaskID, Confidence: 1,
				Status: "confirmed", Version: p.Version + 1, Supersedes: &sup}
			if err = e.insertProfile(ctx, tx, np); err != nil {
				return err
			}
			p = np
		default:
			return fmt.Errorf("%w: action must be confirm, enable, disable, delete or edit", ErrInvalid)
		}
		if err != nil {
			return err
		}
		out = p
		return policy.Audit(ctx, tx, "PROFILE_"+strings.ToUpper(action), "owner", "", "", map[string]any{"id": id, "result_id": p.ID})
	})
	return out, err
}

// profileContext: confirmed preferences as the owner's rules, tentative ones as
// labelled hints. Never for public tasks.
func (e *Engine) profileContext(ctx context.Context, t *task.Task) map[string][]string {
	if t.Scope.IsPublic() {
		return nil
	}
	items, err := e.ListProfile(ctx, false)
	if err != nil {
		return nil
	}
	out := map[string][]string{"confirmed": {}, "tentative": {}}
	for _, p := range items {
		switch p.Status {
		case "confirmed":
			out["confirmed"] = append(out["confirmed"], p.Statement)
		case "tentative":
			out["tentative"] = append(out["tentative"], p.Statement)
		}
	}
	return out
}
