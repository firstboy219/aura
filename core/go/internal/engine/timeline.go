package engine

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// Timeline is the activity view of a task (Blueprint P2 "Activity timeline
// v1"), derived only from Core state and events.
type Timeline struct {
	Task      *task.Task       `json:"task"`
	NextStep  map[string]any   `json:"next_step"`
	Completed []map[string]any `json:"completed"`
	Blockers  []map[string]any `json:"blockers"`
	Decisions []map[string]any `json:"pending_decisions"`
	Spend     map[string]any   `json:"spend"`
	Recent    []task.Event     `json:"recent_events"`
}

// TaskTimeline builds the timeline for one task.
func (e *Engine) TaskTimeline(ctx context.Context, id string) (*Timeline, error) {
	t, err := task.Get(ctx, e.st.R, id)
	if err != nil {
		return nil, err
	}
	tl := &Timeline{Task: t, Completed: []map[string]any{}, Blockers: []map[string]any{}, Decisions: []map[string]any{}}
	steps, err := task.Steps(ctx, e.st.R, id)
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		item := map[string]any{"step_no": s.StepNo, "description": s.Description, "tool_id": s.ToolID, "state": s.State}
		switch s.State {
		case task.StepSucceeded:
			tl.Completed = append(tl.Completed, item)
		case task.StepFailed:
			item["reason"] = deref(s.Reason)
			tl.Blockers = append(tl.Blockers, item)
		case task.StepSkipped:
		default:
			if tl.NextStep == nil {
				tl.NextStep = item
			}
		}
	}
	aps, _ := policy.Pending(ctx, e.st.R)
	for _, ap := range aps {
		if ap.TaskID == id {
			tl.Decisions = append(tl.Decisions, map[string]any{"kind": "approval", "approval_id": ap.ID, "tool_id": ap.ToolID,
				"target": ap.Target, "side_effect": ap.SideEffect, "action_hash": ap.ActionHash, "expires_at": ap.ExpiresAt})
		}
	}
	if q := e.PendingQuestion(ctx, id); q != nil {
		tl.Decisions = append(tl.Decisions, map[string]any{"kind": "question", "question": *q})
	}
	evs, err := task.Events(ctx, e.st.R, id, 0, false)
	if err != nil {
		return nil, err
	}
	for _, ev := range evs {
		switch ev.Type {
		case "CAPABILITY_DENIED", "STEP_ATTEMPT_FAILED", "TOOL_FAILED", "VERIFICATION_FAILED", "TASK_ERROR":
			var d map[string]any
			json.Unmarshal(ev.Data, &d)
			tl.Blockers = append(tl.Blockers, map[string]any{"event": ev.Type, "seq": ev.Seq, "data": d})
		}
	}
	if len(evs) > 10 {
		evs = evs[len(evs)-10:]
	}
	tl.Recent = evs
	tree := t.CostUSD + t.CostReservedUSD
	if n, err := e.Tree(ctx, id); err == nil {
		tree = n.TreeCostUSD
	}
	tl.Spend = map[string]any{"cost_usd": t.CostUSD + t.CostReservedUSD, "tree_cost_usd": tree, "budget_usd": t.Budget.MaxCostUSD}
	return tl, nil
}

// WorkspaceFile is one file of a task's workspace.
type WorkspaceFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// Files lists the regular files in a task's workspace (symlinks are skipped).
func (e *Engine) Files(ctx context.Context, id string) ([]WorkspaceFile, error) {
	if _, err := task.Get(ctx, e.st.R, id); err != nil {
		return nil, err
	}
	root := filepath.Join(e.ws.Root(), id)
	out := []WorkspaceFile{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(filepath.Base(rel), ".tmp-") {
			return nil // in-progress atomic write
		}
		out = append(out, WorkspaceFile{Path: filepath.ToSlash(rel), Bytes: info.Size()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// ReadFile returns one workspace file of a task (path-safe, text, ≤ 1 MiB).
func (e *Engine) ReadFile(ctx context.Context, id, rel string) (string, error) {
	if _, err := task.Get(ctx, e.st.R, id); err != nil {
		return "", err
	}
	r, err := e.ws.Read(id, rel)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
