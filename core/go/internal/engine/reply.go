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
	"slices"
	"strings"
	"time"

	"aura.local/core/internal/budget"
	"aura.local/core/internal/gateway"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/odi"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
	"aura.local/core/internal/taxonomy"
	"aura.local/core/internal/view"
)

const (
	maxReplyChars    = 6000
	failureReplyWait = 90 * time.Second
	failureEvidence  = 6
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
// (unverified_outcome, ODI A2: a write that no check observed; Detail names the evidence level.)
type ReplyNote struct {
	Code   string `json:"code"` // model_knowledge | core_summary | ungrounded_numbers | view_trimmed | task_failed | voice_confirm | screen_designed
	Detail string `json:"detail,omitempty"`
}

// Reply is what AURA told the owner about one task.
type Reply struct {
	TaskID    string       `json:"task_id"`
	Text      string       `json:"text"`
	View      []view.Block `json:"view"`
	Scene     view.Scene   `json:"scene"`
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
	rows, err := e.st.R.QueryContext(ctx, "select task_id, text, view_json, basis, notes_json, files_json, created_at, scene_json from task_replies where task_id in (?"+
		strings.Repeat(",?", len(taskIDs)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r := &Reply{View: []view.Block{}, Notes: []ReplyNote{}, Files: []string{}}
		var v, n, f, sc string
		if err := rows.Scan(&r.TaskID, &r.Text, &v, &r.Basis, &n, &f, &r.CreatedAt, &sc); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(sc), &r.Scene)
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
		and agent_role='main' and state in ('COMPLETED','FAILED','CANCELLED','BUDGET_EXCEEDED')`).Scan(&finished, &completed)
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
	if basis == BasisModel {
		if name := e.designTarget(ctx, t); name != "" {
			return e.designScreen(ctx, t, name)
		}
	}
	if basis == BasisModel && t.Scope.Voice != nil {
		if q := e.askInReply(ctx, t.ID); q != "" {
			return e.storeReply(ctx, t, q, nil, view.Scene{}, BasisModel, []ReplyNote{{Code: "voice_confirm"}}, nil)
		}
	}
	text, blocks, scene, notes, err := e.writeReply(ctx, t, evidence, basis, nil)
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
	return e.storeReply(ctx, t, text, blocks, scene, basis, notes, files)
}

func (e *Engine) storeReply(ctx context.Context, t *task.Task, text string, blocks []view.Block, scene view.Scene, basis string, notes []ReplyNote, files []string) error {
	if blocks == nil {
		blocks = []view.Block{}
	}
	if files == nil {
		files = []string{}
	}
	if notes == nil {
		notes = []ReplyNote{}
	}
	vj, _ := json.Marshal(blocks)
	nj, _ := json.Marshal(notes)
	fj, _ := json.Marshal(files)
	sj, _ := json.Marshal(scene)
	var later []pendingSchedule
	defer func() { e.createSchedules(ctx, t, later) }()
	return e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into task_replies(task_id, text, view_json, basis, notes_json, files_json, created_at, scene_json) values(?,?,?,?,?,?,?,?)
			on conflict(task_id) do update set text=excluded.text, view_json=excluded.view_json, basis=excluded.basis, notes_json=excluded.notes_json,
			files_json=excluded.files_json, created_at=excluded.created_at, scene_json=excluded.scene_json`, t.ID, text, string(vj), basis, string(nj), string(fj), ids.NowMs(), string(sj)); err != nil {
			return err
		}
		types := []string{}
		for _, b := range blocks {
			types = append(types, b.Type)
		}
		kept, err := saveNotes(ctx, tx, t.ID, blocks)
		if err != nil {
			return err
		}
		reminders, sched, err := e.saveSchedules(ctx, tx, t, blocks)
		if err != nil {
			return err
		}
		later = sched
		_, err = task.AppendEvent(ctx, tx, t.ID, nil, "OWNER_REPLY", "core", map[string]any{"basis": basis, "chars": len(text), "view": types, "notes": notes,
			"notes_kept": kept, "reminders_kept": reminders, "schedules": len(sched)})
		return err
	})
}

// taskFailure is what the reply writer is told about a task that did not finish.
type taskFailure struct {
	Code   string `json:"code"`   // canonical reason code
	Detail string `json:"detail"` // the Core's own record of what went wrong
}

// explainable failures are worth a model-written explanation: something went
// wrong while working. Budget, provider, policy and stop reasons are stated by
// the Core itself (no model is available, or none should speak for a policy).
func explainable(code string) bool {
	switch code {
	case taxonomy.ToolFailed, taxonomy.VerificationFailed, taxonomy.ModelOutputInvalid, taxonomy.PlanInvalid, taxonomy.ReplanLimit,
		taxonomy.SiteBlocked:
		return true
	}
	return false
}

// failureReply tells the owner that a task ended without its result, why, and
// what was found on the way (ADR-0055). A failed task used to end in silence.
// The task stays FAILED: this reply is an explanation, never a verified result,
// and is always labelled task_failed. It must not fail the caller.
func (e *Engine) failureReply(parent context.Context, t *task.Task, reason, detail string) {
	if !e.wantsReply(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), failureReplyWait)
	defer cancel()
	code := firstNonEmpty(taxonomy.Canonical(reason), taxonomy.InternalError)
	var evidence []evidenceItem
	if obs, err := e.observations(ctx, t, 1<<30); err == nil {
		for _, o := range obs {
			if o.ToolID != nil && *o.ToolID == "workspace.write" {
				continue
			}
			evidence = append(evidence, evidenceItem{Source: o.Source, Kind: "observation", Content: truncate(o.Content, 4000)})
		}
		if len(evidence) > failureEvidence {
			evidence = evidence[len(evidence)-failureEvidence:]
		}
	}
	note := ReplyNote{Code: "task_failed", Detail: code}
	var text string
	var blocks []view.Block
	var notes []ReplyNote
	scene := view.Scene{Mood: "alert"}
	basis := BasisSummary
	if explainable(code) && parent.Err() == nil {
		var err error
		text, blocks, _, notes, err = e.writeReply(ctx, t, evidence, BasisModel, &taskFailure{Code: code, Detail: truncate(detail, 600)})
		if err != nil {
			e.log.Warn("failure reply writer failed; the Core states the failure", "task_id", t.ID, "err", err)
			text = ""
		} else {
			basis = BasisModel
		}
	}
	if text == "" {
		text, blocks = failureSummary(code, t.Objective, evidence)
		if code == taxonomy.ProviderUnavailable {
			if cause := e.providerCause(t); cause != "" {
				text = "Maaf, tugas ini tidak selesai. / Sorry, this task was not completed.\n" + cause
			}
		}
		notes = []ReplyNote{note}
	}
	if err := e.storeReply(ctx, t, text, blocks, scene, basis, notes, nil); err != nil {
		e.log.Warn("failure reply not stored", "task_id", t.ID, "err", err)
	}
}

var failureTexts = map[string]string{
	taxonomy.ProviderUnavailable: "Tidak ada model yang bisa dipakai untuk tugas ini saat ini. / No model route is usable for this task right now.",
	taxonomy.BudgetExceeded:      "Anggaran tugas atau anggaran harian habis sebelum tugas selesai. / The task or daily budget ran out before the task finished.",
	taxonomy.MaxIterations:       "Batas jumlah langkah tercapai sebelum tugas selesai. / The iteration limit was reached before the task finished.",
	taxonomy.MaxWallTime:         "Batas waktu tugas habis sebelum tugas selesai. / The task ran out of time before it finished.",
	taxonomy.PolicyDenied:        "Tugas ini meminta sesuatu yang tidak diizinkan oleh izin atau kebijakan yang berlaku. / The task asked for something its permissions or policy do not allow.",
	taxonomy.ToolFailed:          "Sebuah alat gagal dan rencana pengganti tidak berhasil. / A tool failed and replanning did not help.",
	taxonomy.VerificationFailed: "Hasil yang dibuat tidak lolos pemeriksaan, jadi tidak saya laporkan sebagai selesai. / " +
		"The result did not pass verification, so it is not reported as done.",
	taxonomy.WorkerCrash: "Proses pekerja berhenti berulang kali. / The worker process stopped repeatedly.",
	taxonomy.SiteBlocked: "Situs tujuan menolak akses otomatis; tidak ada jalan lain yang dicoba. / The site refused automated access; no way around it was tried.",
}

// providerCause states why no model could be used, from what the providers
// themselves answered (gateway.ProviderIssues) and the privacy rule — the facts
// the owner needs in order to act: replace a rejected key, add credit, or wait
// for quota. Empty when the gateway knows no cause.
func (e *Engine) providerCause(t *task.Task) string {
	if e.gw == nil {
		return ""
	}
	var id, en []string
	for _, i := range e.gw.ProviderIssues() {
		msg := view.CleanText(i.Message, 160)
		switch i.Kind {
		case gateway.IssueKeyRejected:
			id = append(id, fmt.Sprintf("Penyedia model %q menolak kunci API AURA (HTTP %d: %s). Tugasnya sendiri tidak salah; kunci itu perlu diganti oleh Tuan%s.", i.Provider, i.Status, msg, e.keyHint(i.Provider)))
			en = append(en, fmt.Sprintf("Model provider %q rejects AURA's API key (HTTP %d: %s). The task itself is not at fault; the owner must replace the key%s.", i.Provider, i.Status, msg, e.keyHint(i.Provider)))
		case gateway.IssueNoCredit:
			id = append(id, fmt.Sprintf("Penyedia model %q melaporkan kredit habis (HTTP %d: %s).", i.Provider, i.Status, msg))
			en = append(en, fmt.Sprintf("Model provider %q reports no credit (HTTP %d: %s).", i.Provider, i.Status, msg))
		case gateway.IssueQuota:
			back, backEN := "", ""
			if d := e.gw.QuotaBackIn(i.Provider); d > 0 {
				m := int(d.Minutes()) + 1
				back, backEN = fmt.Sprintf(" Rute tercepat dicoba lagi sekitar %d menit lagi.", m), fmt.Sprintf(" The soonest route is tried again in about %d min.", m)
			}
			id = append(id, fmt.Sprintf("Kuota penyedia model %q sedang habis (HTTP %d).%s", i.Provider, i.Status, back))
			en = append(en, fmt.Sprintf("Model provider %q is out of quota (HTTP %d).%s", i.Provider, i.Status, backEN))
		}
	}
	if !t.Scope.IsPublic() {
		mayLog := false
		for _, h := range e.gw.Health() {
			mayLog = mayLog || h.MayLog
		}
		if mayLog {
			id = append(id, "Tugas ini privat, jadi rute gratis yang boleh menyimpan prompt tidak dipakai.")
			en = append(en, "This task is private, so free routes that may log prompts were not used.")
		}
	}
	if len(id) == 0 {
		return ""
	}
	return strings.Join(id, " ") + "\n" + strings.Join(en, " ")
}

// keyHint names the command that replaces a provider's key when the key lives in
// the vault (the name is configuration, never the secret).
func (e *Engine) keyHint(provider string) string {
	file := ""
	if provider == "openrouter" {
		file = e.cfg.Providers.OpenRouter.KeyFile
	}
	for _, p := range e.cfg.Providers.OpenAICompatible {
		if p.ID == provider {
			file = p.KeyFile
		}
	}
	if name, ok := strings.CutPrefix(file, "vault:"); ok && name != "" {
		return fmt.Sprintf(" (`aura secrets import %s --stdin`)", name)
	}
	return ""
}

// failureSummary is the Core's own statement of a failure: fixed wording per
// reason code, the last finding verbatim, and a retry suggestion. No model.
func failureSummary(code, objective string, evidence []evidenceItem) (string, []view.Block) {
	why, ok := failureTexts[code]
	if !ok {
		why = "Terjadi kesalahan saat mengerjakan tugas. / Something went wrong while working on the task."
	}
	text := "Maaf, tugas ini tidak selesai. / Sorry, this task was not completed.\n" + why
	blocks := []view.Block{{Type: "callout", Tone: "danger", Text: code}}
	for i := len(evidence) - 1; i >= 0; i-- {
		if c := strings.TrimSpace(evidence[i].Content); c != "" {
			blocks = append(blocks, view.Block{Type: "text", Title: evidence[i].Source, Text: view.CleanText(c, 1500)})
			break
		}
	}
	if o := strings.TrimSpace(objective); o != "" && len(o) <= 600 {
		blocks = append(blocks, view.Block{Type: "actions", Items: []view.Item{{Label: "Coba lagi / Try again", Value: o}}})
	}
	return text, blocks
}

// writeReply asks the worker for the reply. With failure set the writer explains
// a task that did not finish; the reply is then labelled task_failed.
func (e *Engine) writeReply(ctx context.Context, t *task.Task, evidence []evidenceItem, basis string, failure *taskFailure) (string, []view.Block, view.Scene, []ReplyNote, error) {
	none := view.Scene{}
	gen, err := e.supFor(t.ID).Ensure()
	if err != nil {
		return "", nil, none, nil, err
	}
	// The reply writer needs no tool: its grant carries none.
	s := &session{t: t, gen: gen}
	expires := time.Now().Add(time.Duration(t.Budget.MaxWallSeconds) * time.Second).UnixMilli()
	if err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		s.token, s.grant, err = policy.Mint(ctx, tx, t.ID, gen, policy.Scopes{}, expires, nil)
		return err
	}); err != nil {
		return "", nil, none, nil, err
	}
	octx, _ := e.ownerContext(ctx, t)
	guidance, _ := e.brainGuidance(ctx, t)
	params := map[string]any{"guidance": guidance, "objective": e.objectiveFor(ctx, t), "basis": basis,
		"evidence": evidence, "owner_context": octx, "conversation": e.conversation(ctx, t), "self": e.selfBrief(ctx, t),
		"tier": tierFor(t), "view_schema": view.Schema(), "view_types": view.Types, "scene_schema": view.SceneSchema(),
		// A spoken request is answered aloud: the text is what AURA says, the blocks what it shows meanwhile.
		"spoken": t.Scope.Voice != nil, "now": e.localNow()}
	if failure != nil {
		params["failure"] = failure
	}
	raw, err := e.callWorker(ctx, s, "task.reply", params)
	if s.fatal != nil {
		return "", nil, none, nil, s.fatal
	}
	if err != nil {
		return "", nil, none, nil, err
	}
	var r struct {
		Text  string          `json:"text"`
		View  json.RawMessage `json:"view"`
		Scene json.RawMessage `json:"scene"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", nil, none, nil, fmt.Errorf("invalid_reply: %v", err)
	}
	text := view.CleanText(r.Text, maxReplyChars)
	if text == "" {
		return "", nil, none, nil, errors.New("invalid_reply: empty text")
	}
	blocks, removed := view.Sanitize(r.View, view.Options{KnownURL: func(u string) bool { return e.hasProvenance(t, u) }})
	var notes []ReplyNote
	if failure != nil {
		// A failed task keeps nothing and plays nothing.
		blocks = slices.DeleteFunc(blocks, func(b view.Block) bool { return b.Type == "note" || b.Type == "player" || b.Type == "schedule" })
	} else {
		e.illustrate(ctx, t, blocks)
	}
	if failure != nil {
		notes = append(notes, ReplyNote{Code: "task_failed", Detail: failure.Code})
	} else if basis == BasisModel {
		notes = append(notes, ReplyNote{Code: "model_knowledge"})
	} else if missing := ungroundedNumbers(text, blocks, evidence, e.objectiveFor(ctx, t)); len(missing) > 0 {
		notes = append(notes, ReplyNote{Code: "ungrounded_numbers", Detail: strings.Join(missing, ", ")})
	}
	if len(removed) > 0 {
		notes = append(notes, ReplyNote{Code: "view_trimmed", Detail: truncate(strings.Join(removed, "; "), 400)})
	}
	// ODI A2 (ADR-0071): a consequential step that no postcondition check observed must not read as "done and checked".
	if failure == nil {
		if lvl, bad, err := odi.UnverifiedEffects(ctx, e.st.R, t.ID); err == nil && bad {
			notes = append(notes, ReplyNote{Code: "unverified_outcome", Detail: "evidence " + lvl + ": " + map[string]string{odi.V0: "the action was attempted; no success was reported", odi.V1: "the tool reported success; nothing observed the result"}[lvl]})
		}
	}
	if notes == nil {
		notes = []ReplyNote{}
	}
	return text, blocks, view.SanitizeScene(r.Scene), notes, nil
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
