package engine

// Learning L1 (ADR-0025): after a verified success the worker reflects; the
// Core stores a lesson (memory), a fact (memory, evidence-gated) and a skill
// (versioned plan template). Skills never carry permissions.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// Skill is a named, versioned plan template.
type Skill struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Status         string          `json:"status"`
	CurrentVersion int             `json:"current_version"`
	Triggers       string          `json:"triggers"`
	Template       json.RawMessage `json:"template"`
	Notes          string          `json:"notes"`
	SourceTaskID   *string         `json:"source_task_id"`
	UpdatedAt      int64           `json:"updated_at"`
}

type reflection struct {
	Lesson *struct {
		Content string `json:"content"`
	} `json:"lesson"`
	Fact *struct {
		Content       string `json:"content"`
		EvidenceQuote string `json:"evidence_quote"`
	} `json:"fact"`
	Skill *struct {
		Name     string        `json:"name"`
		Triggers []string      `json:"triggers"`
		Steps    []plannedStep `json:"steps"`
		Notes    string        `json:"notes"`
	} `json:"skill"`
	Preference *struct {
		Statement string `json:"statement"`
	} `json:"preference"`
}

// reflect runs after a verified success. Failures only log: learning never
// changes a task's outcome.
func (e *Engine) reflect(ctx context.Context, taskID string, evidence []evidenceItem) {
	t, err := task.Get(ctx, e.st.R, taskID)
	if err != nil || t.State != task.Succeeded {
		return
	}
	steps, _ := task.Steps(ctx, e.st.R, taskID)
	var plan []plannedStep
	for _, s := range steps {
		if s.State != task.StepSucceeded {
			continue
		}
		if ps, err := stepPlan(s); err == nil {
			plan = append(plan, *ps)
		}
	}
	s, err := e.newSession(ctx, t)
	if err != nil {
		return
	}
	octx, _ := e.ownerContext(ctx, t)
	raw, err := e.callWorker(ctx, s, "task.reflect", map[string]any{"objective": t.Objective, "steps": plan, "evidence": evidence,
		"owner_context": octx, "replans": t.Replans, "tier": tierFor(t)})
	// The task is finished: the grant minted for reflection must not outlive the
	// worker call (revoked before anything else is recorded).
	e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error { return policy.RevokeTask(ctx, tx, taskID) })
	var r reflection
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	if err != nil {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			_, werr := task.AppendInternalEvent(ctx, tx, taskID, nil, "REFLECTION_SKIPPED", "core", map[string]any{"reason": truncate(err.Error(), 300)})
			return werr
		})
		return
	}
	learned := map[string]any{}
	if r.Fact != nil && strings.TrimSpace(r.Fact.Content) != "" {
		if id, st, err := e.ProposeMemory(ctx, t, "fact", r.Fact.Content, r.Fact.EvidenceQuote, evidence); err == nil {
			learned["fact"] = map[string]any{"id": id, "status": st}
		}
	}
	if r.Lesson != nil && strings.TrimSpace(r.Lesson.Content) != "" {
		// A lesson describes how the task was done; it comes from a verified task.
		if id, _, err := e.ProposeMemory(ctx, t, "lesson", r.Lesson.Content, "", nil); err == nil {
			e.st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "update memories set status='active' where id=? and status='tentative'", id)
				return err
			})
			learned["lesson"] = id
		}
	}
	if r.Skill != nil && strings.TrimSpace(r.Skill.Name) != "" {
		if sk, err := e.saveSkill(ctx, t, r.Skill.Name, r.Skill.Triggers, r.Skill.Steps, r.Skill.Notes); err == nil {
			learned["skill"] = map[string]any{"id": sk.ID, "version": sk.CurrentVersion}
		} else {
			learned["skill_rejected"] = err.Error()
		}
	}
	if r.Preference != nil && strings.TrimSpace(r.Preference.Statement) != "" && len(octx) > 0 {
		if p, err := e.inferPreference(ctx, t, r.Preference.Statement); err == nil {
			learned["preference"] = p.ID
		}
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, taskID, nil, "REFLECTED", "worker", learned)
		return err
	})
}

var reSkillName = strings.NewReplacer(" ", "-", "/", "-", "_", "-")

// saveSkill creates a skill or adds a new version of it. Templates may use
// only tools from the trusted manifest; skills derived from tasks that were not
// verified are not stored (the caller only reflects on verified tasks).
func (e *Engine) saveSkill(ctx context.Context, t *task.Task, name string, triggers []string, steps []plannedStep, notes string) (*Skill, error) {
	name = strings.ToLower(reSkillName.Replace(strings.TrimSpace(name)))
	if len(name) == 0 || len(name) > 60 || len(steps) == 0 || len(steps) > 12 {
		return nil, fmt.Errorf("%w: skill needs a name and 1..12 steps", ErrInvalid)
	}
	all := name + " " + strings.Join(triggers, " ") + " " + notes
	for _, s := range steps {
		if s.ToolID == nil {
			return nil, fmt.Errorf("%w: skill step without tool", ErrInvalid)
		}
		if _, ok := policy.Lookup(*s.ToolID); !ok {
			return nil, fmt.Errorf("%w: skill uses unknown tool %q", ErrInvalid, *s.ToolID)
		}
		all += " " + s.Description
	}
	if protectedTopic(all) {
		e.st.Write(ctx, func(tx *sql.Tx) error {
			return policy.Audit(ctx, tx, "PROTECTED_PROPOSAL_REJECTED", "worker", t.ID, "", map[string]any{"kind": "skill", "name": name})
		})
		return nil, ErrProtected
	}
	tmpl, _ := json.Marshal(steps)
	trig := truncate(strings.Join(triggers, ", "), 500)
	notes = truncate(notes, 1000)
	var id string
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		now := ids.NowMs()
		var version int
		err := tx.QueryRowContext(ctx, "select id, current_version from skills where name=?", name).Scan(&id, &version)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			id, version = ids.New(), 0
			if _, err := tx.ExecContext(ctx, "insert into skills(id, name, status, current_version, created_at, updated_at) values(?,?,?,?,?,?)",
				id, name, "active", 1, now, now); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			var latest int
			tx.QueryRowContext(ctx, "select max(version) from skill_versions where skill_id=?", id).Scan(&latest)
			version = latest
		}
		version++
		tid := t.ID
		if _, err := tx.ExecContext(ctx, `insert into skill_versions(skill_id, version, triggers, template_json, notes, source_task_id, created_at)
			values(?,?,?,?,?,?,?)`, id, version, trig, string(tmpl), notes, tid, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update skills set current_version=?, updated_at=? where id=?", version, now, id); err != nil {
			return err
		}
		return refreshSkillFTS(ctx, tx, id)
	})
	if err != nil {
		return nil, err
	}
	return e.GetSkill(ctx, id)
}

func refreshSkillFTS(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, "delete from skills_fts where skill_id=?", id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into skills_fts(skill_id, text) select s.id, s.name || ' ' || v.triggers || ' ' || v.notes
		from skills s join skill_versions v on v.skill_id = s.id and v.version = s.current_version where s.id = ?`, id)
	return err
}

const skillQuery = `select s.id, s.name, s.status, s.current_version, v.triggers, v.template_json, v.notes, v.source_task_id, s.updated_at
	from skills s join skill_versions v on v.skill_id = s.id and v.version = s.current_version`

func scanSkill(sc interface{ Scan(...any) error }) (*Skill, error) {
	var s Skill
	var tmpl string
	if err := sc.Scan(&s.ID, &s.Name, &s.Status, &s.CurrentVersion, &s.Triggers, &tmpl, &s.Notes, &s.SourceTaskID, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Template = json.RawMessage(tmpl)
	return &s, nil
}

// GetSkill returns a skill at its current version.
func (e *Engine) GetSkill(ctx context.Context, id string) (*Skill, error) {
	s, err := scanSkill(e.st.R.QueryRowContext(ctx, skillQuery+" where s.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return s, err
}

// ListSkills returns all skills at their current version.
func (e *Engine) ListSkills(ctx context.Context) ([]*Skill, error) {
	rows, err := e.st.R.QueryContext(ctx, skillQuery+" order by s.updated_at desc")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Skill{}
	for rows.Next() {
		s, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SkillVersion is one historical version.
type SkillVersion struct {
	Version      int             `json:"version"`
	Triggers     string          `json:"triggers"`
	Template     json.RawMessage `json:"template"`
	Notes        string          `json:"notes"`
	SourceTaskID *string         `json:"source_task_id"`
	CreatedAt    int64           `json:"created_at"`
}

// SkillVersions lists a skill's versions, newest first.
func (e *Engine) SkillVersions(ctx context.Context, id string) ([]SkillVersion, error) {
	rows, err := e.st.R.QueryContext(ctx, "select version, triggers, template_json, notes, source_task_id, created_at from skill_versions where skill_id=? order by version desc", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SkillVersion
	for rows.Next() {
		var v SkillVersion
		var tmpl string
		rows.Scan(&v.Version, &v.Triggers, &tmpl, &v.Notes, &v.SourceTaskID, &v.CreatedAt)
		v.Template = json.RawMessage(tmpl)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SkillDiff is a line diff between two versions' templates.
func (e *Engine) SkillDiff(ctx context.Context, id string, from, to int) (string, error) {
	vs, err := e.SkillVersions(ctx, id)
	if err != nil {
		return "", err
	}
	var a, b *SkillVersion
	for i := range vs {
		if vs[i].Version == from {
			a = &vs[i]
		}
		if vs[i].Version == to {
			b = &vs[i]
		}
	}
	if a == nil || b == nil {
		return "", task.ErrNotFound
	}
	render := func(v *SkillVersion) []string {
		var steps any
		json.Unmarshal(v.Template, &steps)
		pretty, _ := json.MarshalIndent(map[string]any{"triggers": v.Triggers, "notes": v.Notes, "steps": steps}, "", "  ")
		return strings.Split(string(pretty), "\n")
	}
	return lineDiff(render(a), render(b)), nil
}

// SetSkillStatus disables or enables a skill (owner control).
func (e *Engine) SetSkillStatus(ctx context.Context, id, status string) (*Skill, error) {
	if status != "active" && status != "disabled" {
		return nil, fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
	}
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "update skills set status=?, updated_at=? where id=?", status, ids.NowMs(), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return task.ErrNotFound
		}
		return policy.Audit(ctx, tx, "SKILL_"+strings.ToUpper(status), "owner", "", "", map[string]any{"skill_id": id})
	})
	if err != nil {
		return nil, err
	}
	return e.GetSkill(ctx, id)
}

// RollbackSkill makes an earlier version current again (history is kept).
func (e *Engine) RollbackSkill(ctx context.Context, id string, version int) (*Skill, error) {
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(ctx, "select count(*) from skill_versions where skill_id=? and version=?", id, version).Scan(&n)
		if n == 0 {
			return task.ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "update skills set current_version=?, updated_at=? where id=?", version, ids.NowMs(), id); err != nil {
			return err
		}
		if err := refreshSkillFTS(ctx, tx, id); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "SKILL_ROLLED_BACK", "owner", "", "", map[string]any{"skill_id": id, "version": version})
	})
	if err != nil {
		return nil, err
	}
	return e.GetSkill(ctx, id)
}

// skillContext gives planning up to three matching active skills.
func (e *Engine) skillContext(ctx context.Context, t *task.Task) []map[string]any {
	q := ftsQuery(t.Objective)
	if q == "" {
		return nil
	}
	rows, err := e.st.R.QueryContext(ctx, skillQuery+` join skills_fts f on f.skill_id = s.id where skills_fts match ? and s.status='active'
		order by bm25(skills_fts) limit 3`, q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		s, err := scanSkill(rows)
		if err != nil {
			continue
		}
		var steps any
		json.Unmarshal(s.Template, &steps)
		out = append(out, map[string]any{"name": s.Name, "version": s.CurrentVersion, "triggers": s.Triggers, "steps": steps,
			"notes": s.Notes, "source_task_id": s.SourceTaskID})
	}
	return out
}

// lineDiff is a small LCS line diff ("-" removed, "+" added, " " same).
func lineDiff(a, b []string) string {
	n, m := len(a), len(b)
	l := make([][]int, n+1)
	for i := range l {
		l[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				l[i][j] = l[i+1][j+1] + 1
			} else {
				l[i][j] = max(l[i+1][j], l[i][j+1])
			}
		}
	}
	var sb strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			sb.WriteString("  " + a[i] + "\n")
			i++
			j++
		case l[i+1][j] >= l[i][j+1]:
			sb.WriteString("- " + a[i] + "\n")
			i++
		default:
			sb.WriteString("+ " + b[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		sb.WriteString("- " + a[i] + "\n")
	}
	for ; j < m; j++ {
		sb.WriteString("+ " + b[j] + "\n")
	}
	return sb.String()
}
