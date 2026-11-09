package engine

// Subagents v1 (ADR-0019): agent.delegate creates a child task with a briefing,
// an attenuated grant (tool subset) and a budget carve-out; the parent waits
// until the child finishes; the child's findings return through the Shared Task
// Context. Children run sequentially through the normal scheduler. Only the
// main task may ask the owner questions.

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

const maxDepth = 2

var errDelegationDenied = errors.New("delegation_denied")

// delegate creates the child task and parks the parent on a "tasks" wait.
func (e *Engine) delegate(ctx context.Context, parent *task.Task, st *task.Step, args map[string]any) (string, error) {
	if parent.Depth+1 > maxDepth {
		return "", fmt.Errorf("%w: max_depth", errDelegationDenied)
	}
	var childTools []string
	for _, t := range args["tools"].([]any) {
		if s := t.(string); s != "agent.delegate" || parent.Depth+2 <= maxDepth {
			childTools = append(childTools, s)
		}
	}
	root := parent
	if parent.RootTaskID != parent.ID {
		r, err := task.Get(ctx, e.st.R, parent.RootTaskID)
		if err != nil {
			return "", err
		}
		root = r
	}
	treeSpent, err := e.gov.TreeSpent(ctx, root.ID)
	if err != nil {
		return "", err
	}
	remaining := root.Budget.MaxCostUSD - treeSpent
	if remaining <= 0 {
		return "", fmt.Errorf("%w: budget_carve_out", errDelegationDenied)
	}
	brief := args["brief"].(string)
	var waitID string
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		// Idempotent per step: a re-run after a crash reuses the child.
		if err := tx.QueryRowContext(ctx, "select id from task_waits where step_id=? and satisfied_at is null", st.ID).Scan(&waitID); err == nil {
			return nil
		}
		pid := parent.ID
		child := &task.Task{
			Objective: "[Subagent briefing from task " + parent.ID + "]\n" + brief + "\n\nYou are a specialist subagent of a larger task. " +
				"Work only on this brief, cite your sources, and do not ask the owner questions.",
			ParentTaskID: &pid, RootTaskID: root.ID, AgentRole: "specialist", Depth: parent.Depth + 1,
			Budget: task.Budget{MaxCostUSD: remaining / 2, MaxIterations: max(4, parent.Budget.MaxIterations/2),
				MaxWallSeconds: parent.Budget.MaxWallSeconds},
			Scope:        task.Scope{Tools: childTools, AllowDomains: parent.Scope.AllowDomains, DataClass: parent.Scope.DataClass},
			ConfigHash:   parent.ConfigHash,
			Priority:     parent.Priority + 1, // finish the child before unrelated work
			TierOverride: parent.TierOverride,
		}
		if err := task.Insert(ctx, tx, child); err != nil {
			return err
		}
		waitID = ids.New()
		if _, err := tx.ExecContext(ctx, "insert into task_waits(id, task_id, step_id, kind, key, created_at) values(?,?,?,?,?,?)",
			waitID, parent.ID, st.ID, "tasks", child.ID, ids.NowMs()); err != nil {
			return err
		}
		if err := postContext(ctx, tx, root.ID, parent.ID, "objective", "core_verified", "Delegated to "+child.ID+": "+brief,
			map[string]any{"child_task_id": child.ID, "step_no": st.StepNo}); err != nil {
			return err
		}
		if _, err := task.AppendEvent(ctx, tx, parent.ID, &st.ID, "SUBAGENT_STARTED", "core", map[string]any{
			"child_task_id": child.ID, "tools": childTools, "budget_usd": child.Budget.MaxCostUSD}); err != nil {
			return err
		}
		_, err := task.Transition(ctx, tx, parent.ID, task.Waiting, "", "subagent:"+child.ID, "core")
		return err
	})
	if err == nil {
		e.Wake()
	}
	return waitID, err
}

func postContext(ctx context.Context, tx *sql.Tx, rootID, authorID, kind, trust, content string, prov map[string]any) error {
	b, _ := json.Marshal(prov)
	var author any
	if authorID != "" {
		author = authorID
	}
	_, err := tx.ExecContext(ctx, `insert into shared_context(id, root_task_id, author_task_id, kind, trust, content, provenance_json, created_at)
		values(?,?,?,?,?,?,?,?)`, ids.New(), rootID, author, kind, trust, truncate(content, 20000), string(b), ids.NowMs())
	return err
}

// childFindings summarizes a finished child: its state and outputs (latest
// version of each written file, else its observations). Model-produced, so untrusted.
func (e *Engine) childFindings(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, childID string) (state task.State, text string, sources []string, err error) {
	var reason sql.NullString
	if err = q.QueryRowContext(ctx, "select state, reason from tasks where id=?", childID).Scan(&state, &reason); err != nil {
		return
	}
	rows, err := q.QueryContext(ctx, "select c.state_json, coalesce(c.label,'') from checkpoints c where c.task_id=? and c.label in ('write_intent','observation') order by c.created_at", childID)
	if err != nil {
		return
	}
	files := map[string]string{}
	var order []string
	var observations []obsRecord
	for rows.Next() {
		var raw, label string
		rows.Scan(&raw, &label)
		if label == "write_intent" {
			var in writeIntent
			if json.Unmarshal([]byte(raw), &in) == nil {
				if _, ok := files[in.Path]; !ok {
					order = append(order, in.Path)
				}
				files[in.Path] = in.Content
			}
		} else {
			var o obsRecord
			if json.Unmarshal([]byte(raw), &o) == nil {
				observations = append(observations, o)
				sources = append(sources, o.Source)
			}
		}
	}
	rows.Close()
	var sb strings.Builder
	fmt.Fprintf(&sb, "Subagent %s finished: %s", childID, state)
	if reason.Valid && reason.String != "" {
		fmt.Fprintf(&sb, " (%s)", reason.String)
	}
	sb.WriteString("\n")
	for _, p := range order {
		fmt.Fprintf(&sb, "\n--- %s ---\n%s\n", p, truncate(files[p], 8000))
	}
	if len(order) == 0 {
		for _, o := range observations {
			fmt.Fprintf(&sb, "\n--- %s ---\n%s\n", o.Source, truncate(o.Content, 3000))
		}
	}
	return state, sb.String(), sources, nil
}

// AddContext records an owner correction or instruction on a task tree. Every
// task in the tree sees it at its next step (owner trust), so a plan change
// reaches active subagents.
func (e *Engine) AddContext(ctx context.Context, taskID, content string) (*task.Task, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 4000 {
		return nil, fmt.Errorf("%w: content must be 1..4000 characters", ErrInvalid)
	}
	t, err := e.owner(ctx, taskID, func(tx *sql.Tx, t *task.Task) error {
		if t.State.Terminal() {
			return fmt.Errorf("%w: task is %s", task.ErrInvalidTransition, t.State)
		}
		if err := postContext(ctx, tx, t.RootTaskID, "", "owner_correction", "owner", content, map[string]any{"via_task": taskID}); err != nil {
			return err
		}
		_, err := task.AppendEvent(ctx, tx, t.RootTaskID, nil, "OWNER_CORRECTION", "owner", map[string]any{"chars": len(content), "via_task": taskID})
		return err
	})
	if err == nil {
		e.Wake()
	}
	return t, err
}

// TreeNode is a task with its descendants and rolled-up cost.
type TreeNode struct {
	Task         *task.Task  `json:"task"`
	Children     []*TreeNode `json:"children"`
	TreeCostUSD  float64     `json:"tree_cost_usd"`
	ContextItems int         `json:"context_items,omitempty"`
}

// Tree returns the task tree rooted at id with rolled-up cost.
func (e *Engine) Tree(ctx context.Context, id string) (*TreeNode, error) {
	t, err := task.Get(ctx, e.st.R, id)
	if err != nil {
		return nil, err
	}
	n := &TreeNode{Task: t, TreeCostUSD: t.CostUSD + t.CostReservedUSD, Children: []*TreeNode{}}
	kids, err := task.Children(ctx, e.st.R, id)
	if err != nil {
		return nil, err
	}
	for _, k := range kids {
		c, err := e.Tree(ctx, k.ID)
		if err != nil {
			return nil, err
		}
		n.Children = append(n.Children, c)
		n.TreeCostUSD += c.TreeCostUSD
	}
	if t.RootTaskID == t.ID {
		e.st.R.QueryRowContext(ctx, "select count(*) from shared_context where root_task_id=?", id).Scan(&n.ContextItems)
	}
	return n, nil
}

// descendants lists all non-terminal descendants of a task.
func descendants(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `with recursive d(id) as (select id from tasks where parent_task_id=?
		union all select t.id from tasks t join d on t.parent_task_id = d.id)
		select d.id from d join tasks t on t.id = d.id where t.state not in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED')`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var x string
		rows.Scan(&x)
		out = append(out, x)
	}
	return out, rows.Err()
}

func cancelDescendants(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	ds, err := descendants(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if _, err := task.Transition(ctx, tx, d, task.Cancelled, "ancestor_cancelled", "", "owner"); err != nil {
			return nil, err
		}
		if err := policy.RevokeTask(ctx, tx, d); err != nil {
			return nil, err
		}
	}
	return ds, nil
}
