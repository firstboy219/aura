package odi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/store"
)

const maxDiff = 256 << 10

// Actor is who acts on a change: the API builds it from the authenticated principal. A model, worker or agent is never an owner.
type Actor struct {
	Name   string
	Owner  bool
	StepUp bool // a fresh second factor was verified for this request
}

// Change is one self-engineering proposal.
type Change struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Level      string   `json:"level"`
	Reasons    []string `json:"level_reasons"`
	Components []string `json:"components"`
	Proposal   Proposal `json:"proposal"`
	DiffSHA256 string   `json:"diff_sha256,omitempty"`
	Weakening  []string `json:"weakening"`
	ProposedBy string   `json:"proposed_by"`
	Status     string   `json:"status"`
	ApprovedBy string   `json:"approved_by,omitempty"`
	ApprovedAt *int64   `json:"approved_at,omitempty"`
	Note       string   `json:"decision_note,omitempty"`
	Evidence   []Evid   `json:"evidence"`
	CreatedAt  int64    `json:"created_at"`
	UpdatedAt  int64    `json:"updated_at"`
}

// Evid is post-deployment evidence recorded with a status change.
type Evid struct {
	Kind   string `json:"kind"` // e.g. post_deploy_health, test_run, canary_metrics
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	At     int64  `json:"at"`
}

var (
	ErrChange    = errors.New("change_invalid")
	ErrForbidden = errors.New("change_forbidden")
)

// Changes is the proposal store.
type Changes struct {
	St *store.Store
	// L1Preapproved reports whether L1 changes are auto-approved (the owner's preapproved boundary); nil: they need the owner.
	L1Preapproved func(ctx context.Context) bool
}

// Create classifies, validates and records a proposal. L0 (and pre-approved L1) changes with no weakening findings are approved automatically after the checks;
// everything else waits for its approver. A proposal that weakens its tests is recorded as blocked.
func (c *Changes) Create(ctx context.Context, by Actor, p Proposal, diff string) (*Change, error) {
	level, reasons, err := Classify(p.Components)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChange, err)
	}
	if bad := p.Validate(level); len(bad) > 0 {
		return nil, fmt.Errorf("%w: missing %v", ErrChange, bad)
	}
	if len(diff) > maxDiff {
		return nil, fmt.Errorf("%w: diff larger than %d bytes", ErrChange, maxDiff)
	}
	weak := WeakensTests(diff)
	pre := c.L1Preapproved != nil && c.L1Preapproved(ctx)
	rule := RuleFor(level, pre)
	ch := &Change{ID: ids.New(), Title: p.Title, Level: level, Reasons: reasons, Components: p.Components, Proposal: p, Weakening: weak, ProposedBy: by.Name,
		Status: "awaiting_approval", Evidence: []Evid{}, CreatedAt: ids.NowMs(), UpdatedAt: ids.NowMs()}
	if diff != "" {
		h := sha256.Sum256([]byte(diff))
		ch.DiffSHA256 = hex.EncodeToString(h[:])
	}
	switch {
	case len(weak) > 0:
		ch.Status, ch.Note = "blocked", "the diff weakens its own evidence: "+weak[0]
	case rule.AutoAfterChecks:
		now := ids.NowMs()
		ch.Status, ch.ApprovedBy, ch.ApprovedAt = "approved", "auto:"+level+"-validated", &now
	}
	pj, _ := json.Marshal(p)
	cj, _ := json.Marshal(p.Components)
	rj, _ := json.Marshal(reasons)
	wj, _ := json.Marshal(weak)
	err = c.St.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into change_proposals(id, title, level, level_reasons_json, components_json, proposal_json, diff_text, diff_sha256, weakening_json, proposed_by, status,
			approved_by, approved_at, decision_note, created_at, updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ch.ID, ch.Title, level, string(rj), string(cj), string(pj), nullIf(diff), nullIf(ch.DiffSHA256), string(wj), by.Name, ch.Status, nullIf(ch.ApprovedBy), ch.ApprovedAt, nullIf(ch.Note), ch.CreatedAt, ch.UpdatedAt); err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "CHANGE_PROPOSED", by.Name, "", ch.ID, map[string]any{"level": level, "status": ch.Status, "components": p.Components, "weakening": weak, "diff_sha256": ch.DiffSHA256})
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Get loads one change.
func (c *Changes) Get(ctx context.Context, id string) (*Change, error) {
	var ch Change
	var pj, cj, rj, wj, ej string
	var by, note sql.NullString
	var diff sql.NullString
	err := c.St.R.QueryRowContext(ctx, `select id, title, level, level_reasons_json, components_json, proposal_json, diff_sha256, weakening_json, proposed_by, status, approved_by, approved_at, decision_note,
		evidence_json, created_at, updated_at from change_proposals where id=?`, id).Scan(&ch.ID, &ch.Title, &ch.Level, &rj, &cj, &pj, &diff, &wj, &ch.ProposedBy, &ch.Status, &by, &ch.ApprovedAt, &note, &ej, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: not found", ErrChange)
	}
	json.Unmarshal([]byte(rj), &ch.Reasons)
	json.Unmarshal([]byte(cj), &ch.Components)
	json.Unmarshal([]byte(pj), &ch.Proposal)
	json.Unmarshal([]byte(wj), &ch.Weakening)
	json.Unmarshal([]byte(ej), &ch.Evidence)
	ch.DiffSHA256, ch.ApprovedBy, ch.Note = diff.String, by.String, note.String
	return &ch, nil
}

// List returns recent changes, newest first.
func (c *Changes) List(ctx context.Context, status string) ([]*Change, error) {
	q, args := "select id from change_proposals", []any{}
	if status != "" {
		q, args = q+" where status=?", append(args, status)
	}
	rows, err := c.St.R.QueryContext(ctx, q+" order by created_at desc limit 100", args...)
	if err != nil {
		return nil, err
	}
	var idsList []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		idsList = append(idsList, id)
	}
	rows.Close()
	out := []*Change{}
	for _, id := range idsList {
		if ch, err := c.Get(ctx, id); err == nil {
			out = append(out, ch)
		}
	}
	return out, nil
}

// Decide approves or rejects a change that awaits approval. The rules: the owner for L1 (unless pre-approved), L2 and L3; a fresh second factor for L3; never the
// proposer unless they are the owner; never a change that weakens its tests.
func (c *Changes) Decide(ctx context.Context, id string, by Actor, approve bool, note string) (*Change, error) {
	var out *Change
	err := c.St.Write(ctx, func(tx *sql.Tx) error {
		ch, err := c.Get(ctx, id)
		if err != nil {
			return err
		}
		if ch.Status != "awaiting_approval" {
			return fmt.Errorf("%w: the change is %s", ErrChange, ch.Status)
		}
		now := ids.NowMs()
		status := "rejected"
		if approve {
			pre := c.L1Preapproved != nil && c.L1Preapproved(ctx)
			rule := RuleFor(ch.Level, pre)
			switch {
			case len(ch.Weakening) > 0:
				return fmt.Errorf("%w: the diff weakens its own evidence", ErrForbidden)
			case rule.OwnerRequired && !by.Owner:
				return fmt.Errorf("%w: %s changes need the owner", ErrForbidden, ch.Level)
			case rule.StepUpRequired && !by.StepUp:
				return fmt.Errorf("%w: %s changes need the owner with a fresh second factor", ErrForbidden, ch.Level)
			case strings.HasPrefix(ch.ProposedBy, "agent:") && (ch.Level == L2 || ch.Level == L3) && !by.StepUp:
				// an automated proposer may hold the owner's API token; a human's fresh second factor is what makes the approval the owner's
				return fmt.Errorf("%w: a change proposed by an agent needs the owner's fresh second factor from L2", ErrForbidden)
			case by.Name == ch.ProposedBy && !by.Owner:
				return fmt.Errorf("%w: nobody approves their own proposal", ErrForbidden)
			}
			status = "approved"
		} else if !by.Owner && by.Name != ch.ProposedBy {
			return fmt.Errorf("%w: only the owner or the proposer can reject", ErrForbidden)
		}
		if _, err := tx.ExecContext(ctx, "update change_proposals set status=?, approved_by=?, approved_at=?, decision_note=?, updated_at=? where id=?", status, by.Name, now, nullIf(note), now, id); err != nil {
			return err
		}
		ch.Status, ch.ApprovedBy, ch.ApprovedAt, ch.Note, ch.UpdatedAt = status, by.Name, &now, note, now
		out = ch
		return policy.Audit(ctx, tx, "CHANGE_"+map[bool]string{true: "APPROVED", false: "REJECTED"}[approve], by.Name, "", id, map[string]any{"level": ch.Level, "note": note, "step_up": by.StepUp})
	})
	return out, err
}

// Move records a status change (canary, deployed, rolled back, accepted) with its evidence. Deploying needs an approved change; acceptance needs evidence of
// the deployment and the owner; a rollback needs a reason. AURA's own deployment tools do the deploying: this records and checks.
func (c *Changes) Move(ctx context.Context, id string, by Actor, to, note string, ev []Evid) (*Change, error) {
	var out *Change
	err := c.St.Write(ctx, func(tx *sql.Tx) error {
		ch, err := c.Get(ctx, id)
		if err != nil {
			return err
		}
		if !CanMove(ch.Status, to) {
			return fmt.Errorf("%w: %s -> %s is not a legal move", ErrChange, ch.Status, to)
		}
		switch to {
		case "accepted":
			good := false
			for _, e := range ev {
				if e.Kind == "post_deploy_health" && e.OK {
					good = true
				}
			}
			if !good {
				return fmt.Errorf("%w: acceptance needs post_deploy_health evidence with ok=true", ErrChange)
			}
			if !by.Owner {
				return fmt.Errorf("%w: only the owner accepts a change", ErrForbidden)
			}
		case "rolled_back":
			if note == "" {
				return fmt.Errorf("%w: a rollback needs a reason", ErrChange)
			}
		case "in_canary", "deployed":
			if ch.Level != L0 && !by.Owner && ch.Status != "in_canary" {
				return fmt.Errorf("%w: only the owner starts the rollout of an %s change", ErrForbidden, ch.Level)
			}
		}
		now := ids.NowMs()
		for i := range ev {
			ev[i].At = now
		}
		all := append(ch.Evidence, ev...)
		ej, _ := json.Marshal(all)
		if _, err := tx.ExecContext(ctx, "update change_proposals set status=?, evidence_json=?, decision_note=coalesce(?, decision_note), updated_at=? where id=?", to, string(ej), nullIf(note), now, id); err != nil {
			return err
		}
		ch.Status, ch.Evidence, ch.UpdatedAt = to, all, now
		out = ch
		return policy.Audit(ctx, tx, "CHANGE_"+upper(to), by.Name, "", id, map[string]any{"level": ch.Level, "note": note, "evidence": len(ev)})
	})
	return out, err
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
