package engine

// Owner replies (ADR-0055). A task used to end with files in a workspace and a
// state; the owner had to go and read them. Now every root task ends with a
// reply: what AURA tells the owner, plus an adaptive view the clients draw
// natively. The reply is written by the worker from the task's evidence (or,
// for a plan with no steps, from the model's own knowledge — and labelled so),
// sanitised and checked by the Core, and stored with the task.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/view"
)

const (
	maxReplyChars    = 6000
	conversationSpan = 24 * time.Hour
	conversationMax  = 6
)

// Reply bases: where the content of a reply comes from.
const (
	BasisEvidence = "evidence"        // written from what this task read, computed or wrote
	BasisModel    = "model_knowledge" // no tool was used; not checked against any source
	BasisSummary  = "summary"         // composed by the Core because no model could write it
)

// ReplyNote is an honest caveat the Core attaches to a reply; clients show it.
type ReplyNote struct {
	Code   string `json:"code"` // model_knowledge | core_summary | ungrounded_numbers | view_trimmed
	Detail string `json:"detail,omitempty"`
}

// Reply is what AURA told the owner about one task.
type Reply struct {
	TaskID    string       `json:"task_id"`
	Text      string       `json:"text"`
	View      []view.Block `json:"view"`
	Basis     string       `json:"basis"`
	Notes     []ReplyNote  `json:"notes"`
	Files     []string     `json:"files"`
	CreatedAt int64        `json:"created_at"`
}

// wantsReply: root tasks of an owner get a reply; subagents report to their
// parent, and workflows and experiments have their own outputs.
func (e *Engine) wantsReply(t *task.Task) bool {
	return e.replies && t.ParentTaskID == nil && t.Scope.Reply != "none" && t.Scope.Workflow == ""
}

// mayAnswer: the planner may return no steps (answer from the model's own
// knowledge) only when the objective asks for nothing a tool must produce.
func (e *Engine) mayAnswer(t *task.Task) bool {
	return e.wantsReply(t) && len(t.Scope.Attachments) == 0 && len(ExpectedOutputs(t.Objective, nil)) == 0 &&
		RequiredDelegations(t.Objective) == 0 && len(ObjectiveURLs(t.Objective)) == 0
}

// GetReply returns the stored reply of a task (nil if it has none).
func (e *Engine) GetReply(ctx context.Context, taskID string) (*Reply, error) {
	rs, err := e.Replies(ctx, []string{taskID})
	if err != nil {
		return nil, err
	}
	return rs[taskID], nil
}

// Replies returns the stored replies of several tasks, by task id.
func (e *Engine) Replies(ctx context.Context, taskIDs []string) (map[string]*Reply, error) {
	out := map[string]*Reply{}
	if len(taskIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		args[i] = id
	}
	rows, err := e.st.R.QueryContext(ctx, "select task_id, text, view_json, basis, notes_json, files_json, created_at from task_replies where task_id in (?"+
		strings.Repeat(",?", len(taskIDs)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r := &Reply{View: []view.Block{}, Notes: []ReplyNote{}, Files: []string{}}
		var v, n, f string
		if err := rows.Scan(&r.TaskID, &r.Text, &v, &r.Basis, &n, &f, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(v), &r.View)
		json.Unmarshal([]byte(n), &r.Notes)
		json.Unmarshal([]byte(f), &r.Files)
		out[r.TaskID] = r
	}
	return out, rows.Err()
}

// conversationKey groups tasks into one owner conversation: the app account
// behind the device token, else the member, else the workspace owner.
func (e *Engine) conversationKey(ctx context.Context, taskID string) string {
	var key string
	e.st.R.QueryRowContext(ctx, `select coalesce(m.account_id, t.created_by, '') from tasks t left join members m on m.id = t.created_by
		where t.id=?`, taskID).Scan(&key)
	return key
}

// conversation is the recent exchange between this owner and AURA, oldest
// first, so a follow-up ("explain the second one") can be understood. A public
// task sees only public exchanges: private content never reaches a route that
// may log prompts.
func (e *Engine) conversation(ctx context.Context, t *task.Task) []map[string]string {
	q := `select t.objective, r.text from task_replies r join tasks t on t.id = r.task_id left join members m on m.id = t.created_by
		where t.parent_task_id is null and t.id <> ? and coalesce(m.account_id, t.created_by, '') = ? and r.created_at >= ?`
	if t.Scope.IsPublic() {
		q += ` and json_extract(t.scope_json, '$.data_class') = 'public'`
	}
	rows, err := e.st.R.QueryContext(ctx, q+" order by r.created_at desc limit ?", t.ID, e.conversationKey(ctx, t.ID),
		time.Now().Add(-conversationSpan).UnixMilli(), conversationMax)
	if err != nil {
		e.log.Warn("conversation context unavailable", "task_id", t.ID, "err", err)
		return nil
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var o, r string
		rows.Scan(&o, &r)
		out = append([]map[string]string{{"owner": truncate(o, 300), "aura": truncate(r, 700)}}, out...)
	}
	return out
}

// selfBrief is a short, deterministic description of AURA for the planner and
// the reply writer: every fact comes from the Core's records, none from a model.
func (e *Engine) selfBrief(ctx context.Context, t *task.Task) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are AURA (Autonomous Universal Runtime Agent), version %s, working for this owner. You are software: you have no "+
		"consciousness or feelings and never claim them; what you do have is a self-model measured from your own records.\n", firstNonEmpty(e.version, "dev"))
	fmt.Fprintf(&sb, "Workspace time now: %s.\n", time.Now().In(e.cfg.Location()).Format("Monday 2006-01-02 15:04 MST"))
	fmt.Fprintf(&sb, "Tools you may use in this task: %s.\n", strings.Join(t.Scope.Tools, ", "))
	if e.gw != nil {
		usable, resting := map[string]int{}, map[string]int{}
		for _, h := range e.gw.Health() {
			if h.MayLog && !t.Scope.IsPublic() {
				continue
			}
			if h.Resting {
				resting[h.Tier]++
			} else {
				usable[h.Tier]++
			}
		}
		var parts []string
		for _, tier := range []string{"fast", "standard", "deep"} {
			parts = append(parts, fmt.Sprintf("%s %d usable/%d resting", tier, usable[tier], resting[tier]))
		}
		fmt.Fprintf(&sb, "Model routes available to this task right now: %s.\n", strings.Join(parts, "; "))
	}
	var finished, completed, open int
	e.st.R.QueryRowContext(ctx, `select count(*), coalesce(sum(state='COMPLETED'),0) from tasks where parent_task_id is null
		and state in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED')`).Scan(&finished, &completed)
	e.st.R.QueryRowContext(ctx, "select count(distinct title) from limitations where status in ('open','diagnosing','remedy_proposed','escalated')").Scan(&open)
	fmt.Fprintf(&sb, "Your record: %d owner tasks finished, %d completed. Known limitations on record: %d.\n", finished, completed, open)
	sb.WriteString("How you improve: a limitation is recorded only from measured evidence, diagnosed, remedied inside your permission class " +
		"(a new skill, a sandboxed tool, a prompt variant), re-tested and rolled back if it does not help. You cannot change your own " +
		"permissions, policies, Charter or Core; the owner approves anything beyond that.")
	return sb.String()
}

// reply writes and stores the owner reply. For a task that used tools a model
// failure is not a task failure: the Core composes a plain summary instead.
// A direct answer has nothing else to show, so there the error is returned.
func (e *Engine) reply(ctx context.Context, t *task.Task, evidence []evidenceItem) error {
	basis := BasisEvidence
	if len(evidence) == 0 {
		basis = BasisModel
	}
	var files []string
	for _, ev := range evidence {
		if ev.Kind == "file" {
			files = append(files, ev.Source)
		}
	}
	text, blocks, notes, err := e.writeReply(ctx, t, evidence, basis)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, budget.ErrHalted) {
			return err
		}
		if basis == BasisModel {
			return err
		}
		e.log.Warn("reply writer failed; composing a summary", "task_id", t.ID, "err", err)
		text, blocks = summaryReply(evidence, files)
		basis, notes = BasisSummary, []ReplyNote{{Code: "core_summary", Detail: reasonCode(err)}}
	}
	if blocks == nil {
		blocks = []view.Block{}
	}
	if files == nil {
		files = []string{}
	}
	vj, _ := json.Marshal(blocks)
	nj, _ := json.Marshal(notes)
	fj, _ := json.Marshal(files)
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into task_replies(task_id, text, view_json, basis, notes_json, files_json, created_at) values(?,?,?,?,?,?,?)
			on conflict(task_id) do update set text=excluded.text, view_json=excluded.view_json, basis=excluded.basis, notes_json=excluded.notes_json,
			files_json=excluded.files_json, created_at=excluded.created_at`, t.ID, text, string(vj), basis, string(nj), string(fj), ids.NowMs()); err != nil {
			return err
		}
		types := []string{}
		for _, b := range blocks {
			types = append(types, b.Type)
		}
		_, err := task.AppendEvent(ctx, tx, t.ID, nil, "OWNER_REPLY", "core", map[string]any{"basis": basis, "chars": len(text), "view": types, "notes": notes})
		return err
	})
}

func (e *Engine) writeReply(ctx context.Context, t *task.Task, evidence []evidenceItem, basis string) (string, []view.Block, []ReplyNote, error) {
	gen, err := e.supFor(t.ID).Ensure()
	if err != nil {
		return "", nil, nil, err
	}
	// The reply writer needs no tool: its grant carries none.
	s := &session{t: t, gen: gen}
	expires := time.Now().Add(time.Duration(t.Budget.MaxWallSeconds) * time.Second).UnixMilli()
	if err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		s.token, s.grant, err = policy.Mint(ctx, tx, t.ID, gen, policy.Scopes{}, expires, nil)
		return err
	}); err != nil {
		return "", nil, nil, err
	}
	octx, _ := e.ownerContext(ctx, t)
	guidance, _ := e.brainGuidance(ctx, t)
	raw, err := e.callWorker(ctx, s, "task.reply", map[string]any{"guidance": guidance, "objective": t.Objective, "basis": basis,
		"evidence": evidence, "owner_context": octx, "conversation": e.conversation(ctx, t), "self": e.selfBrief(ctx, t),
		"tier": tierFor(t), "view_schema": view.Schema(), "view_types": view.Types})
	if s.fatal != nil {
		return "", nil, nil, s.fatal
	}
	if err != nil {
		return "", nil, nil, err
	}
	var r struct {
		Text string          `json:"text"`
		View json.RawMessage `json:"view"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", nil, nil, fmt.Errorf("invalid_reply: %v", err)
	}
	text := view.CleanText(r.Text, maxReplyChars)
	if text == "" {
		return "", nil, nil, errors.New("invalid_reply: empty text")
	}
	blocks, removed := view.Sanitize(r.View, view.Options{KnownURL: func(u string) bool { return e.hasProvenance(t, u) }})
	var notes []ReplyNote
	if basis == BasisModel {
		notes = append(notes, ReplyNote{Code: "model_knowledge"})
	} else if missing := ungroundedNumbers(text, blocks, evidence, t.Objective); len(missing) > 0 {
		notes = append(notes, ReplyNote{Code: "ungrounded_numbers", Detail: strings.Join(missing, ", ")})
	}
	if len(removed) > 0 {
		notes = append(notes, ReplyNote{Code: "view_trimmed", Detail: truncate(strings.Join(removed, "; "), 400)})
	}
	if notes == nil {
		notes = []ReplyNote{}
	}
	return text, blocks, notes, nil
}

// summaryReply is the Core's own plain reply: the files written and the last
// result, verbatim. No model is involved, so nothing in it is invented.
func summaryReply(evidence []evidenceItem, files []string) (string, []view.Block) {
	var blocks []view.Block
	text := "Tugas selesai. / Task completed."
	if len(files) > 0 {
		b := view.Block{Type: "list", Title: "Files"}
		for _, f := range files {
			b.Items = append(b.Items, view.Item{Label: f})
		}
		blocks = append(blocks, b)
	}
	for i := len(evidence) - 1; i >= 0; i-- {
		if c := strings.TrimSpace(evidence[i].Content); c != "" {
			blocks = append(blocks, view.Block{Type: "text", Title: evidence[i].Source, Text: view.CleanText(c, 1500)})
			break
		}
	}
	return text, blocks
}

var numberRe = regexp.MustCompile(`\d[\d.,]*\d|\d`)

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ungroundedNumbers lists numbers (three or more digits) that the reply states
// but that appear nowhere in the task's evidence or objective. Model confidence
// is not evidence: the owner is told which figures the Core could not trace.
func ungroundedNumbers(text string, blocks []view.Block, evidence []evidenceItem, objective string) []string {
	var hay strings.Builder
	add := func(s string) {
		for _, m := range numberRe.FindAllString(s, -1) {
			hay.WriteString(" " + digitsOf(m) + " ")
		}
	}
	add(objective)
	for _, ev := range evidence {
		add(ev.Content)
		add(ev.Source)
	}
	known := hay.String()
	var said strings.Builder
	said.WriteString(text)
	for _, b := range blocks {
		said.WriteString(" " + b.Title + " " + b.Text)
		for _, r := range b.Rows {
			said.WriteString(" " + strings.Join(r, " "))
		}
		for _, it := range b.Items {
			said.WriteString(" " + it.Label + " " + it.Value + " " + it.Note)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range numberRe.FindAllString(said.String(), -1) {
		d := strings.TrimLeft(digitsOf(m), "0")
		if len(d) < 3 || seen[d] {
			continue
		}
		seen[d] = true
		// A figure counts as traced when its digits appear in the evidence, also as
		// the leading part of a longer figure (3.14 against 3.14159).
		if !strings.Contains(known, " "+d) && len(out) < 8 {
			out = append(out, m)
		}
	}
	return out
}
