package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"aura.local/core/internal/task"
)

// Spoken objectives (ADR-0058). The device's recognizer sends its hypotheses;
// the planner reconstructs what the owner meant (misheard words, colloquial
// Indonesian, mixed languages) and returns it as heard_as. The Core keeps the
// owner's words as the objective and passes the understanding alongside it to
// every later module, so the plan, the steps, the verifier and the reply all
// work on the same reading. Understanding grants nothing: voice stays an input
// channel, and approvals are still given by hand against the action hash.

type heardAs struct {
	HeardAs    string `json:"heard_as"`
	Transcript string `json:"transcript"`
}

// noteHeardAs stores the planner's reading of a spoken objective as content
// (a checkpoint) and records only metadata in the timeline.
func (e *Engine) noteHeardAs(ctx context.Context, t *task.Task, p *planDoc) error {
	if p.HeardAs == nil {
		return nil
	}
	h := strings.TrimSpace(*p.HeardAs)
	p.HeardAs = nil
	if t.Scope.Voice == nil || h == "" {
		return nil // only a spoken objective is reinterpreted
	}
	if len(h) > 2000 {
		h = h[:2000]
	}
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, "", "heard", heardAs{HeardAs: h, Transcript: t.Objective})
		if err != nil {
			return err
		}
		_, err = task.AppendEvent(ctx, tx, t.ID, nil, "VOICE_UNDERSTOOD", "worker", map[string]any{
			"checkpoint": ck, "corrected": !sameWords(h, t.Objective), "alternatives": len(t.Scope.Voice.Alternatives)})
		return err
	})
}

// HeardAs returns what AURA understood a spoken objective to mean, or "".
func (e *Engine) HeardAs(ctx context.Context, taskID string) string {
	var raw string
	if err := e.st.R.QueryRowContext(ctx, "select state_json from checkpoints where task_id=? and label='heard' order by created_at desc limit 1",
		taskID).Scan(&raw); err != nil {
		return ""
	}
	var h heardAs
	if json.Unmarshal([]byte(raw), &h) != nil {
		return ""
	}
	return h.HeardAs
}

// objectiveFor is the objective the worker modules receive: the owner's words,
// and for a spoken objective also what AURA understood them to mean.
func (e *Engine) objectiveFor(ctx context.Context, t *task.Task) string {
	if t.Scope.Voice == nil {
		return t.Objective
	}
	h := e.HeardAs(ctx, t.ID)
	if h == "" || sameWords(h, t.Objective) {
		return t.Objective
	}
	o := h + "\n(The owner said this aloud; the speech recognizer heard: \"" + t.Objective + "\". Work on the meaning above.)"
	if len(o) > 8000 {
		return t.Objective
	}
	return o
}

// sameWords compares two texts ignoring case, punctuation and spacing.
func sameWords(a, b string) bool {
	norm := func(s string) string {
		return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
		}), " ")
	}
	return norm(a) == norm(b)
}

// askInReply is the confirmation question a spoken request's plan asks as its reply.
func (e *Engine) askInReply(ctx context.Context, taskID string) string {
	var raw string
	if err := e.st.R.QueryRowContext(ctx, "select state_json from checkpoints where task_id=? and label='plan' order by created_at desc limit 1",
		taskID).Scan(&raw); err != nil {
		return ""
	}
	var p planDoc
	if json.Unmarshal([]byte(raw), &p) != nil || p.AskInReply == nil {
		return ""
	}
	return strings.TrimSpace(*p.AskInReply)
}
