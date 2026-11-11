package engine

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"aura.local/core/internal/browser"
	"aura.local/core/internal/egress"
	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/shellwatch"
	"aura.local/core/internal/task"
	"aura.local/core/internal/tools"
)

// Server commands (ADR-0065, owner instruction of 2026-11-11). AURA runs
// commands in the aura-shell sandbox: its own container with no secrets, no
// AURA data and no Docker socket, reaching the internet only through the
// Core's egress proxy (any public host; never private or metadata addresses).
// Every command is assessed by the watcher (internal/shellwatch) before it
// runs, and every decision and run is written to shell_log with its output.

type shellState struct {
	socket    string
	proxy     *egress.Proxy
	proxyAddr string // host:port of the egress proxy as the sandbox sees it
	timeout   int
}

// ErrShellUnavailable means server commands are not configured.
var ErrShellUnavailable = errors.New("tool_failed: server commands are not configured (shell.socket)")

// SetShell enables shell.run (app wiring).
func (e *Engine) SetShell(socket string, p *egress.Proxy, proxyAddr string, timeoutSeconds int) {
	if timeoutSeconds <= 0 || timeoutSeconds > 600 {
		timeoutSeconds = 120
	}
	e.shell = &shellState{socket: socket, proxy: p, proxyAddr: proxyAddr, timeout: timeoutSeconds}
}

type shellArgs struct {
	Command  string   `json:"command"`
	FilesIn  []string `json:"files_in"`
	FilesOut []string `json:"files_out"`
	TimeoutS int      `json:"timeout_s"`
}

const maxShellFiles = 10

// validateShellArgs checks a shell.run call against its planned step. The
// command may be written at step time when the plan left it empty (it usually
// depends on earlier output); files and timeout must be as planned.
func (e *Engine) validateShellArgs(s *session, c toolCall) (map[string]any, string, string) {
	var a shellArgs
	if strictDecode(c.Arguments, &a) != nil {
		return nil, "", "invalid_arguments"
	}
	a.Command = strings.TrimSpace(a.Command)
	if a.Command == "" || len(a.Command) > shellwatch.MaxCommand || len(a.FilesIn) > maxShellFiles || len(a.FilesOut) > maxShellFiles || a.TimeoutS < 0 || a.TimeoutS > 600 {
		return nil, "", "invalid_arguments"
	}
	if s.plan == nil {
		return nil, "", "arguments_differ_from_plan"
	}
	var planned shellArgs
	if s.plan.Input != nil && strings.TrimSpace(*s.plan.Input) != "" {
		if json.Unmarshal([]byte(*s.plan.Input), &planned) != nil {
			return nil, "", "arguments_differ_from_plan"
		}
	}
	if p := strings.TrimSpace(planned.Command); p != "" && p != a.Command {
		return nil, "", "arguments_differ_from_plan"
	}
	if !sameStrings(planned.FilesIn, a.FilesIn) || !sameStrings(planned.FilesOut, a.FilesOut) || (planned.TimeoutS != 0 && planned.TimeoutS != a.TimeoutS) {
		return nil, "", "arguments_differ_from_plan"
	}
	var in, out []any
	for _, p := range a.FilesIn {
		cp, err := tools.CleanPath(p)
		if err != nil {
			return nil, "", "bad_path"
		}
		in = append(in, cp)
	}
	for _, p := range a.FilesOut {
		cp, err := tools.CleanPath(p)
		if err != nil {
			return nil, "", "bad_path"
		}
		out = append(out, cp)
	}
	args := map[string]any{"command": a.Command, "files_in": orEmpty(in), "files_out": orEmpty(out), "timeout_s": a.TimeoutS}
	return args, truncate(a.Command, 300), ""
}

func orEmpty(x []any) []any {
	if x == nil {
		return []any{}
	}
	return x
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ShellRecord is one row of the shell log.
type ShellRecord struct {
	ID          string         `json:"id"`
	TaskID      string         `json:"task_id"`
	StepID      string         `json:"step_id,omitempty"`
	Command     string         `json:"command"`
	Verdict     string         `json:"verdict"`
	Reasons     []string       `json:"reasons"`
	Status      string         `json:"status"` // refused | awaiting_approval | running | ran | timeout | error
	ApprovalID  string         `json:"approval_id,omitempty"`
	ExitCode    *int           `json:"exit_code"`
	DurationMs  *int64         `json:"duration_ms"`
	Stdout      string         `json:"stdout"`
	Stderr      string         `json:"stderr"`
	Redacted    bool           `json:"redacted"`
	FilesIn     []string       `json:"files_in"`
	FilesOut    []string       `json:"files_out"`
	Hosts       map[string]int `json:"hosts"`
	Denied      int64          `json:"egress_denied"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	FinishedAt  *int64         `json:"finished_at"`
	StdoutBytes int64          `json:"stdout_bytes"`
	StderrBytes int64          `json:"stderr_bytes"`
}

func stringsOf(x any) []string {
	var out []string
	if l, ok := x.([]any); ok {
		for _, v := range l {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// logShellDecision records a command the watcher refused or sent for approval.
func (e *Engine) logShellDecision(ctx context.Context, t *task.Task, st *task.Step, args map[string]any, status, approvalID string) {
	cmd, _ := args["command"].(string)
	v := shellwatch.Assess(cmd)
	rj, _ := json.Marshal(v.Reasons)
	fi, _ := json.Marshal(stringsOf(args["files_in"]))
	fo, _ := json.Marshal(stringsOf(args["files_out"]))
	stepID := ""
	if st != nil {
		stepID = st.ID
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `insert into shell_log(id, task_id, step_id, command, verdict, reasons_json, status, approval_id, files_in_json,
			files_out_json, created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, ids.New(), t.ID, stepID, cmd, v.Level, string(rj), status, approvalID,
			string(fi), string(fo), ids.NowMs())
		if err != nil {
			return err
		}
		return policy.Audit(ctx, tx, "SHELL_"+strings.ToUpper(status), "core", t.ID, "", map[string]any{"verdict": v.Level, "reasons": v.Reasons,
			"command": truncate(cmd, 500), "approval_id": approvalID})
	})
}

type shellResult struct {
	ExitCode    int    `json:"exit_code"`
	TimedOut    bool   `json:"timed_out"`
	DurationMs  int64  `json:"duration_ms"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	StdoutBytes int64  `json:"stdout_bytes"`
	StderrBytes int64  `json:"stderr_bytes"`
	Files       []struct {
		Path          string `json:"path"`
		Missing       bool   `json:"missing"`
		TooLarge      bool   `json:"too_large"`
		Bytes         int64  `json:"bytes"`
		ContentBase64 string `json:"content_base64"`
	} `json:"files"`
}

// executeShell runs an authorized shell.run call and logs it.
func (e *Engine) executeShell(ctx context.Context, t *task.Task, st *task.Step, args map[string]any) (any, error) {
	if e.shell == nil {
		e.toolFailed(ctx, t, st, "shell.run", ErrShellUnavailable)
		return nil, ErrShellUnavailable
	}
	cmd, _ := args["command"].(string)
	v := shellwatch.Assess(cmd)
	if v.Level == shellwatch.Blocked { // defence in depth: the tier check refused it already
		e.logShellDecision(ctx, t, st, args, "refused", "")
		err := errors.New("tool_failed: the watcher refused this command")
		e.toolFailed(ctx, t, st, "shell.run", err)
		return nil, err
	}
	filesIn, filesOut := stringsOf(args["files_in"]), stringsOf(args["files_out"])
	timeout := e.shell.timeout
	if n, ok := args["timeout_s"].(int); ok && n > 0 {
		timeout = n
	} else if f, ok := args["timeout_s"].(float64); ok && f > 0 {
		timeout = int(f)
	}
	logID := ids.New()
	rj, _ := json.Marshal(v.Reasons)
	fi, _ := json.Marshal(filesIn)
	fo, _ := json.Marshal(filesOut)
	e.st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `insert into shell_log(id, task_id, step_id, command, verdict, reasons_json, status, files_in_json, files_out_json,
			created_at) values(?,?,?,?,?,?,?,?,?,?)`, logID, t.ID, st.ID, cmd, v.Level, string(rj), "running", string(fi), string(fo), ids.NowMs()); err != nil {
			return err
		}
		_, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_STARTED", "core", map[string]any{"tool_id": "shell.run", "idempotency_key": st.IdempotencyKey,
			"target": truncate(cmd, 300), "verdict": v.Level, "reasons": v.Reasons})
		return err
	})
	finish := func(status string, res *shellResult, sess *egress.Session, errText string) {
		var hosts map[string]int
		var denied int64
		if sess != nil {
			hosts, denied = sess.Hosts(), sess.Denied.Load()
		}
		hj, _ := json.Marshal(hosts)
		var exit any
		var dur any
		out, errOut := "", ""
		var outB, errB int64
		redacted := false
		if res != nil {
			exit, dur = res.ExitCode, res.DurationMs
			var r1, r2 bool
			out, r1 = shellwatch.Redact(res.Stdout)
			errOut, r2 = shellwatch.Redact(res.Stderr)
			redacted = r1 || r2
			outB, errB = res.StdoutBytes, res.StderrBytes
		}
		e.st.Write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `update shell_log set status=?, exit_code=?, duration_ms=?, stdout=?, stderr=?, stdout_bytes=?, stderr_bytes=?,
				redacted=?, hosts_json=?, egress_denied=?, error=?, finished_at=? where id=?`, status, exit, dur, truncate(out, 16000), truncate(errOut, 16000),
				outB, errB, boolInt(redacted), string(hj), denied, errText, ids.NowMs(), logID)
			return err
		})
	}
	// Files go in from the task workspace.
	if filesOut == nil {
		filesOut = []string{} // the host expects lists, never null
	}
	in := []map[string]any{}
	for _, p := range filesIn {
		b, err := e.ws.ReadBytes(t.ID, p, 20<<20)
		if err != nil {
			err = fmt.Errorf("tool_failed: cannot copy %s into the sandbox: %v", p, err)
			finish("error", nil, nil, err.Error())
			e.toolFailed(ctx, t, st, "shell.run", err)
			return nil, err
		}
		in = append(in, map[string]any{"path": p, "content_base64": base64.StdEncoding.EncodeToString(b)})
	}
	sess := &egress.Session{ID: "x-" + randomToken()[:24], Token: randomToken(), TaskID: t.ID, AnyPublic: true}
	proxy := ""
	if e.shell.proxy != nil && e.shell.proxyAddr != "" {
		e.shell.proxy.Add(sess)
		defer e.shell.proxy.Remove(sess.ID)
		proxy = "http://" + sess.ID + ":" + sess.Token + "@" + e.shell.proxyAddr
	}
	c := browser.New(e.shell.socket)
	c.MaxWait = time.Duration(timeout+60) * time.Second
	defer c.Close()
	var res shellResult
	err := c.Call(ctx, "shell.exec", map[string]any{"task_id": t.ID, "run_id": logID, "command": cmd, "timeout_s": timeout,
		"files_in": in, "files_out": filesOut, "proxy": proxy}, &res)
	if err != nil {
		err = fmt.Errorf("tool_failed: shell host: %v", err)
		finish("error", nil, sess, err.Error())
		e.toolFailed(ctx, t, st, "shell.run", err)
		return nil, err
	}
	status := "ran"
	if res.TimedOut {
		status = "timeout"
	}
	// Files come back into the task workspace.
	var saved, notes []string
	for _, f := range res.Files {
		switch {
		case f.Missing:
			notes = append(notes, f.Path+": not produced")
		case f.TooLarge:
			notes = append(notes, f.Path+": too large to copy back")
		default:
			b, derr := base64.StdEncoding.DecodeString(f.ContentBase64)
			if derr == nil {
				if _, werr := e.ws.Write(t.ID, f.Path, string(b)); werr == nil {
					saved = append(saved, f.Path)
					continue
				} else {
					notes = append(notes, f.Path+": "+werr.Error())
					continue
				}
			}
			notes = append(notes, f.Path+": unreadable")
		}
	}
	finish(status, &res, sess, "")
	out, _ := shellwatch.Redact(res.Stdout)
	errOut, _ := shellwatch.Redact(res.Stderr)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Server command (sandbox; output is untrusted data). Watcher: %s", v.Level)
	if len(v.Reasons) > 0 {
		fmt.Fprintf(&sb, " (%s)", strings.Join(v.Reasons, "; "))
	}
	fmt.Fprintf(&sb, "\n$ %s\n", cmd)
	if res.TimedOut {
		fmt.Fprintf(&sb, "TIMED OUT after %d s and was killed.\n", timeout)
	}
	fmt.Fprintf(&sb, "exit code %d, %.1f s\n", res.ExitCode, float64(res.DurationMs)/1000)
	if out != "" {
		fmt.Fprintf(&sb, "--- stdout (%d bytes) ---\n%s\n", res.StdoutBytes, truncate(out, 9000))
	}
	if errOut != "" {
		fmt.Fprintf(&sb, "--- stderr (%d bytes) ---\n%s\n", res.StderrBytes, truncate(errOut, 3000))
	}
	if len(saved) > 0 {
		fmt.Fprintf(&sb, "Saved to the workspace: %s\n", strings.Join(saved, ", "))
	}
	if len(notes) > 0 {
		fmt.Fprintf(&sb, "Files: %s\n", strings.Join(notes, "; "))
	}
	if h := sess.Hosts(); len(h) > 0 {
		names := make([]string, 0, len(h))
		for k := range h {
			names = append(names, k)
		}
		sort.Strings(names)
		fmt.Fprintf(&sb, "Network: %s\n", strings.Join(names, ", "))
	}
	rec := obsRecord{Source: "shell:" + truncate(cmd, 120), Content: truncate(sb.String(), maxObservationChars)}
	summary := map[string]any{"tool_id": "shell.run", "exit_code": res.ExitCode, "timed_out": res.TimedOut, "duration_ms": res.DurationMs,
		"verdict": v.Level, "saved": saved, "log_id": logID}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
		if err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// KillShell stops every running command (kill switch).
func (e *Engine) KillShell(ctx context.Context) {
	if e.shell == nil {
		return
	}
	c := browser.New(e.shell.socket)
	defer c.Close()
	c.Call(ctx, "shell.kill_all", map[string]any{}, nil)
}

// ShellLog lists logged commands, newest first; taskID filters to one task.
func (e *Engine) ShellLog(ctx context.Context, taskID string, limit int) ([]ShellRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `select id, task_id, coalesce(step_id,''), command, verdict, reasons_json, status, coalesce(approval_id,''), exit_code, duration_ms,
		coalesce(stdout,''), coalesce(stderr,''), redacted, files_in_json, files_out_json, coalesce(hosts_json,'{}'), egress_denied, coalesce(error,''),
		created_at, finished_at, stdout_bytes, stderr_bytes from shell_log`
	var qa []any
	if taskID != "" {
		q += " where task_id=?"
		qa = append(qa, taskID)
	}
	q += " order by created_at desc limit ?"
	qa = append(qa, limit)
	rows, err := e.st.R.QueryContext(ctx, q, qa...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShellRecord{}
	for rows.Next() {
		var r ShellRecord
		var reasons, fi, fo, hosts string
		var red int
		if err := rows.Scan(&r.ID, &r.TaskID, &r.StepID, &r.Command, &r.Verdict, &reasons, &r.Status, &r.ApprovalID, &r.ExitCode, &r.DurationMs,
			&r.Stdout, &r.Stderr, &red, &fi, &fo, &hosts, &r.Denied, &r.Error, &r.CreatedAt, &r.FinishedAt, &r.StdoutBytes, &r.StderrBytes); err != nil {
			return nil, err
		}
		r.Redacted = red == 1
		json.Unmarshal([]byte(reasons), &r.Reasons)
		json.Unmarshal([]byte(fi), &r.FilesIn)
		json.Unmarshal([]byte(fo), &r.FilesOut)
		json.Unmarshal([]byte(hosts), &r.Hosts)
		out = append(out, r)
	}
	return out, rows.Err()
}
