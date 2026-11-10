package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/policy"
	"aura.local/core/internal/task"
)

// L2 tools (Blueprint 8A.1, D.1 P4, ADR-0038): small pure Python functions that
// AURA writes to close a missing-tool limitation (remedy R3). They run only in
// the WASM sandbox (no network, no host files, bounded memory and time), are
// checked statically and tested there, and become usable only after the owner
// approves the exact source (bound by its sha256). They never get capabilities:
// the Core passes their input and records their output.

var l2NameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,38}$`)

// l2Allowed is the standard library an L2 tool may import: pure computation only.
var l2Allowed = []string{"base64", "bisect", "collections", "csv", "dataclasses", "datetime", "decimal", "difflib", "enum", "fractions",
	"functools", "hashlib", "heapq", "html", "io", "itertools", "json", "math", "operator", "random", "re", "statistics", "string",
	"textwrap", "typing", "unicodedata"}

// l2CheckerPy runs in the sandbox and inspects a tool's source with ast.
const l2CheckerPy = `
import ast, json, sys
ALLOWED = set(json.loads(sys.argv[1]) if len(sys.argv) > 1 else [])
FORBIDDEN = {"exec", "eval", "compile", "__import__", "open", "input", "breakpoint", "globals", "locals", "vars",
             "getattr", "setattr", "delattr", "memoryview", "exit", "quit", "help", "__builtins__", "__loader__", "__spec__"}
d = json.load(sys.stdin)
src, allowed = d["source"], set(d["allowed"])
problems, imports = [], set()
try:
    tree = ast.parse(src)
except SyntaxError as e:
    print(json.dumps({"ok": False, "problems": ["syntax error: %s (line %s)" % (e.msg, e.lineno)], "imports": []}))
    raise SystemExit(0)
for n in ast.walk(tree):
    if isinstance(n, ast.Import):
        for a in n.names:
            imports.add(a.name.split(".")[0])
    elif isinstance(n, ast.ImportFrom):
        if n.level:
            problems.append("relative imports are not allowed")
        imports.add((n.module or "").split(".")[0])
    elif isinstance(n, ast.Name) and n.id in FORBIDDEN:
        problems.append("forbidden name: " + n.id)
    elif isinstance(n, ast.Attribute) and n.attr.startswith("__"):
        problems.append("dunder attribute access: " + n.attr)
bad = sorted(i for i in imports if i not in allowed)
if bad:
    problems.append("imports outside the allowed standard library set: " + ", ".join(bad))
runs = [n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "run"]
if not runs or len(runs[0].args.args) != 1:
    problems.append("define a top-level function run(input) with exactly one argument")
print(json.dumps({"ok": not problems, "problems": sorted(set(problems))[:20], "imports": sorted(imports)}))
`

// l2RunnerPy is the trusted harness that calls a tool's run(input).
const l2RunnerPy = `
import json, sys
d = json.load(sys.stdin)
ns = {"__name__": "aura_l2_tool"}
try:
    exec(compile(d["source"], "tool.py", "exec"), ns)
    out = ns["run"](d["input"])
    s = json.dumps({"ok": True, "output": out}, ensure_ascii=False, allow_nan=False)
except Exception as e:
    s = json.dumps({"ok": False, "error": (type(e).__name__ + ": " + str(e))[:500]})
sys.stdout.write(s)
`

// L2Tool is one version of an AURA-built tool.
type L2Tool struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      int             `json:"version"`
	Status       string          `json:"status"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	Source       string          `json:"source"`
	SourceSHA256 string          `json:"source_sha256"`
	Tests        []L2Test        `json:"tests"`
	Report       map[string]any  `json:"report"`
	LimitationID *string         `json:"limitation_id"`
	RemedyID     *string         `json:"remedy_id"`
	DecidedBy    *string         `json:"decided_by"`
	CreatedAt    int64           `json:"created_at"`
}

// L2Test is an input with its exact expected output.
type L2Test struct {
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

// L2Draft is what the model proposes.
type L2Draft struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Source      string          `json:"source"`
	Tests       []L2Test        `json:"tests"`
}

// ErrL2Unavailable means the sandbox is not configured.
var ErrL2Unavailable = errors.New("L2 tools need the WASM sandbox (l2.python_wasm)")

func sourceHash(name, source string, schema json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(name + "\x00" + string(schema) + "\x00" + source))
	return hex.EncodeToString(h.Sum(nil))
}

// checkL2Source runs the static check in the sandbox.
func (e *Engine) checkL2Source(ctx context.Context, source string) ([]string, []string, error) {
	in, _ := json.Marshal(map[string]any{"source": source, "allowed": l2Allowed})
	res, err := e.sbx.RunPython(ctx, l2CheckerPy, in)
	if err != nil {
		return nil, nil, fmt.Errorf("static check: %w", err)
	}
	var out struct {
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
		Imports  []string `json:"imports"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, nil, fmt.Errorf("static check output: %w", err)
	}
	return out.Problems, out.Imports, nil
}

// runL2 runs a tool's run(input) in the sandbox.
func (e *Engine) runL2(ctx context.Context, source string, input any) (json.RawMessage, time.Duration, error) {
	in, err := json.Marshal(map[string]any{"source": source, "input": input})
	if err != nil {
		return nil, 0, err
	}
	res, err := e.sbx.RunPython(ctx, l2RunnerPy, in)
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		OK     bool            `json:"ok"`
		Output json.RawMessage `json:"output"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, res.Duration, fmt.Errorf("tool printed invalid output: %w", err)
	}
	if !out.OK {
		return nil, res.Duration, fmt.Errorf("tool raised %s", out.Error)
	}
	return out.Output, res.Duration, nil
}

// jsonEqual compares two JSON values; numbers are equal within 1e-9 relative.
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return valEqual(x, y)
}

func valEqual(x, y any) bool {
	switch xv := x.(type) {
	case float64:
		yv, ok := y.(float64)
		return ok && (xv == yv || math.Abs(xv-yv) <= 1e-9*math.Max(math.Abs(xv), math.Abs(yv)))
	case map[string]any:
		yv, ok := y.(map[string]any)
		if !ok || len(xv) != len(yv) {
			return false
		}
		for k, v := range xv {
			if !valEqual(v, yv[k]) {
				return false
			}
		}
		return true
	case []any:
		yv, ok := y.([]any)
		if !ok || len(xv) != len(yv) {
			return false
		}
		for i := range xv {
			if !valEqual(xv[i], yv[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(x, y)
}

// validateL2 runs the gates of an L2 draft: name and schema, static check,
// sandbox tests (each 3 times: deterministic output, timing), benchmark.
func (e *Engine) validateL2(ctx context.Context, d *L2Draft) (map[string]any, []string) {
	var problems []string
	if !l2NameRe.MatchString(d.Name) {
		problems = append(problems, "name must be snake_case, 2-39 characters")
	}
	var schema map[string]any
	if json.Unmarshal(d.InputSchema, &schema) != nil || schema["type"] != "object" {
		problems = append(problems, "input_schema must be a JSON schema object with type \"object\"")
	}
	if len(d.Source) == 0 || len(d.Source) > 20000 {
		problems = append(problems, "source must be 1..20000 characters")
	}
	if len(d.Tests) < 3 || len(d.Tests) > 10 {
		problems = append(problems, "provide 3..10 tests with exact expected outputs")
	}
	report := map[string]any{"capabilities": []string{}, "allowed_imports": l2Allowed}
	if len(problems) > 0 {
		report["problems"] = problems
		return report, problems
	}
	static, imports, err := e.checkL2Source(ctx, d.Source)
	if err != nil {
		problems = append(problems, err.Error())
	}
	problems = append(problems, static...)
	report["imports"], report["static_check"] = imports, map[string]any{"ok": len(static) == 0 && err == nil, "problems": static}
	if len(problems) > 0 {
		report["problems"] = problems
		return report, problems
	}
	var durations []float64
	var results []map[string]any
	for i, tc := range d.Tests {
		var input any
		if json.Unmarshal(tc.Input, &input) != nil {
			problems = append(problems, fmt.Sprintf("test %d: input is not JSON", i+1))
			continue
		}
		var first json.RawMessage
		r := map[string]any{"test": i + 1}
		for run := 0; run < 3; run++ {
			out, dur, err := e.runL2(ctx, d.Source, input)
			durations = append(durations, float64(dur.Microseconds())/1000)
			if err != nil {
				r["error"] = err.Error()
				problems = append(problems, fmt.Sprintf("test %d: %v", i+1, err))
				break
			}
			if run == 0 {
				first = out
				r["output"] = json.RawMessage(truncate(string(out), 500))
				if !jsonEqual(out, tc.Expected) {
					r["ok"] = false
					problems = append(problems, fmt.Sprintf("test %d: expected %s, got %s", i+1, truncate(string(tc.Expected), 200), truncate(string(out), 200)))
					break
				}
			} else if !jsonEqual(out, first) {
				problems = append(problems, fmt.Sprintf("test %d: output is not deterministic", i+1))
				break
			}
			r["ok"] = true
		}
		results = append(results, r)
	}
	sort.Float64s(durations)
	if len(durations) > 0 {
		report["benchmark"] = map[string]any{"runs": len(durations), "p50_ms": durations[len(durations)/2], "max_ms": durations[len(durations)-1],
			"note": "wall time per sandboxed run, including interpreter start"}
	}
	report["tests"] = results
	if len(problems) > 0 {
		report["problems"] = problems
	}
	return report, problems
}

// saveL2Draft stores a validated draft awaiting the owner's approval.
func (e *Engine) saveL2Draft(ctx context.Context, d *L2Draft, report map[string]any, limID, remedyID string) (*L2Tool, error) {
	var out *L2Tool
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		var v int
		tx.QueryRowContext(ctx, "select coalesce(max(version),0)+1 from l2_tools where name=?", d.Name).Scan(&v)
		tests, _ := json.Marshal(d.Tests)
		id, now := ids.New(), ids.NowMs()
		sum := sourceHash(d.Name, d.Source, d.InputSchema)
		if _, err := tx.ExecContext(ctx, `insert into l2_tools(id, name, version, status, description, input_schema_json, source, source_sha256,
			tests_json, report_json, limitation_id, remedy_id, created_at, updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, d.Name, v, "awaiting_approval", truncate(d.Description, 500), string(d.InputSchema), d.Source, sum, string(tests), js(report),
			nullable(limID), nullable(remedyID), now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "insert into notifications(id, kind, title, body_json, created_at) values(?,?,?,?,?)",
			ids.New(), "info", truncate("New L2 tool awaiting your approval: l2."+d.Name, 200),
			js(map[string]any{"tool_id": id, "name": "l2." + d.Name, "version": v, "sha256": sum, "review": "aura tools show " + id}), now); err != nil {
			return err
		}
		if err := policy.Audit(ctx, tx, "L2_TOOL_PROPOSED", "core", "", "", map[string]any{"tool_id": id, "name": d.Name, "version": v, "sha256": sum}); err != nil {
			return err
		}
		var err error
		out, err = getL2(ctx, tx, id)
		return err
	})
	return out, err
}

const l2Cols = `id, name, version, status, description, input_schema_json, source, source_sha256, tests_json, report_json,
	limitation_id, remedy_id, decided_by, created_at`

func scanL2(sc interface{ Scan(...any) error }) (*L2Tool, error) {
	var t L2Tool
	var schema, tests, report string
	if err := sc.Scan(&t.ID, &t.Name, &t.Version, &t.Status, &t.Description, &schema, &t.Source, &t.SourceSHA256, &tests, &report,
		&t.LimitationID, &t.RemedyID, &t.DecidedBy, &t.CreatedAt); err != nil {
		return nil, err
	}
	t.InputSchema = json.RawMessage(schema)
	json.Unmarshal([]byte(tests), &t.Tests)
	json.Unmarshal([]byte(report), &t.Report)
	return &t, nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func getL2(ctx context.Context, q rowQuerier, id string) (*L2Tool, error) {
	t, err := scanL2(q.QueryRowContext(ctx, "select "+l2Cols+" from l2_tools where id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return t, err
}

// ActiveL2 returns the active version of a tool by its tool id ("l2.<name>").
func (e *Engine) ActiveL2(ctx context.Context, toolID string) (*L2Tool, error) {
	name := strings.TrimPrefix(toolID, "l2.")
	t, err := scanL2(e.st.R.QueryRowContext(ctx, "select "+l2Cols+" from l2_tools where name=? and status='active'", name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, task.ErrNotFound
	}
	return t, err
}

// GetL2 returns one tool version.
func (e *Engine) GetL2(ctx context.Context, id string) (*L2Tool, error) {
	return getL2(ctx, e.st.R, id)
}

// ListL2 lists tool versions, newest first.
func (e *Engine) ListL2(ctx context.Context) ([]*L2Tool, error) {
	rows, err := e.st.R.QueryContext(ctx, "select "+l2Cols+" from l2_tools order by created_at desc limit 200")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*L2Tool
	for rows.Next() {
		t, err := scanL2(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RegisterActiveL2 makes every active L2 tool available (at startup).
func (e *Engine) RegisterActiveL2(ctx context.Context) int {
	if e.sbx == nil {
		return 0
	}
	tools, err := e.ListL2(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for _, t := range tools {
		if t.Status == "active" {
			policy.RegisterL2Tool("l2."+t.Name, t.Version)
			n++
		}
	}
	return n
}

// DecideL2 is the owner's decision on a proposed tool. Approval must name the
// exact source hash shown to the owner; it activates the tool (superseding the
// previous version) and starts the remedy's re-test.
func (e *Engine) DecideL2(ctx context.Context, id string, approve bool, hash, by string) (*L2Tool, error) {
	var out *L2Tool
	var remedyID, limID string
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		t, err := getL2(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Status != "awaiting_approval" {
			return fmt.Errorf("%w: tool is %s, not awaiting approval", task.ErrInvalidTransition, t.Status)
		}
		now := ids.NowMs()
		if !approve {
			if _, err := tx.ExecContext(ctx, "update l2_tools set status='rejected', decided_by=?, decided_at=?, updated_at=? where id=?", by, now, now, id); err != nil {
				return err
			}
			if t.RemedyID != nil {
				tx.ExecContext(ctx, "update limitation_remedies set status='escalated', description=description || ' The owner rejected the proposed tool.', updated_at=? where id=?", now, *t.RemedyID)
				tx.ExecContext(ctx, "update limitations set status='escalated', updated_at=? where id=?", now, deref(t.LimitationID))
			}
			if err := policy.Audit(ctx, tx, "L2_TOOL_REJECTED", by, "", "", map[string]any{"tool_id": id, "name": t.Name}); err != nil {
				return err
			}
			out, err = getL2(ctx, tx, id)
			return err
		}
		if hash != t.SourceSHA256 {
			return fmt.Errorf("%w: the approved hash does not match the tool's source", policy.ErrMismatch)
		}
		if _, err := tx.ExecContext(ctx, "update l2_tools set status='superseded', updated_at=? where name=? and status='active'", now, t.Name); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "update l2_tools set status='active', decided_by=?, decided_at=?, updated_at=? where id=?", by, now, now, id); err != nil {
			return err
		}
		if err := policy.Audit(ctx, tx, "L2_TOOL_APPROVED", by, "", "", map[string]any{"tool_id": id, "name": t.Name, "version": t.Version, "sha256": t.SourceSHA256}); err != nil {
			return err
		}
		remedyID, limID = deref(t.RemedyID), deref(t.LimitationID)
		out, err = getL2(ctx, tx, id)
		return err
	})
	if err != nil || !approve {
		return out, err
	}
	policy.RegisterL2Tool("l2."+out.Name, out.Version)
	if remedyID != "" {
		if err := e.startL2Retest(ctx, remedyID, limID, out); err != nil {
			e.log.Warn("L2 re-test", "tool", out.Name, "err", err)
		}
	}
	return out, nil
}

// DisableL2 stops an active tool (owner decision or rollback).
func (e *Engine) DisableL2(ctx context.Context, id, by, reason string) (*L2Tool, error) {
	var out *L2Tool
	err := e.st.Write(ctx, func(tx *sql.Tx) error {
		t, err := getL2(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Status != "active" {
			return fmt.Errorf("%w: tool is %s", task.ErrInvalidTransition, t.Status)
		}
		now := ids.NowMs()
		if _, err := tx.ExecContext(ctx, "update l2_tools set status='disabled', updated_at=? where id=?", now, id); err != nil {
			return err
		}
		if err := policy.Audit(ctx, tx, "L2_TOOL_DISABLED", by, "", "", map[string]any{"tool_id": id, "name": t.Name, "reason": truncate(reason, 300)}); err != nil {
			return err
		}
		out, err = getL2(ctx, tx, id)
		return err
	})
	if err == nil {
		policy.UnregisterL2Tool("l2." + out.Name)
	}
	return out, err
}

// executeL2 runs an approved tool as a task step. The planned input is passed
// as is; with "path" the Core adds that workspace file's text as file_text.
func (e *Engine) executeL2(ctx context.Context, t *task.Task, st *task.Step, spec policy.ToolSpec, args map[string]any) (any, error) {
	fail := func(err error) (any, error) {
		if !strings.HasPrefix(err.Error(), "tool_failed") {
			err = fmt.Errorf("tool_failed: %v", err)
		}
		e.toolFailed(ctx, t, st, spec.ID, err)
		return nil, err
	}
	if e.sbx == nil {
		return fail(ErrL2Unavailable)
	}
	tool, err := e.ActiveL2(ctx, spec.ID)
	if err != nil {
		return fail(fmt.Errorf("%s is not an active L2 tool", spec.ID))
	}
	input, _ := args["arguments"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}
	if p, ok := args["path"].(string); ok && p != "" {
		f, err := e.ws.Read(t.ID, p)
		if err != nil {
			return fail(fmt.Errorf("reading %s: %v", p, err))
		}
		input["file_text"] = f.Content
	}
	e.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := task.AppendEvent(ctx, tx, t.ID, &st.ID, "TOOL_STARTED", "core", map[string]any{"tool_id": spec.ID, "version": tool.Version,
			"sha256": tool.SourceSHA256, "idempotency_key": st.IdempotencyKey, "sandbox": "wasm"})
		return err
	})
	out, dur, err := e.runL2(ctx, tool.Source, input)
	if err != nil {
		return fail(err)
	}
	rec := obsRecord{Source: spec.ID, Content: truncate(string(out), maxObservationChars)}
	summary := map[string]any{"tool_id": spec.ID, "version": tool.Version, "bytes": len(out), "ms": dur.Milliseconds()}
	err = e.st.Write(ctx, func(tx *sql.Tx) error {
		ck, err := putCheckpoint(ctx, tx, t.ID, st.ID, "observation", rec)
		if err != nil {
			return err
		}
		return task.UpdateStep(ctx, tx, st, st.State, ck, "", "", "TOOL_SUCCEEDED", summary)
	})
	return summary, err
}

// validateL2Args checks an L2 step's arguments against the plan.
func validateL2Args(s *session, c toolCall) (map[string]any, string, string) {
	var a struct {
		Arguments map[string]any `json:"arguments"`
		Path      *string        `json:"path"`
	}
	if strictDecode(c.Arguments, &a) != nil || a.Arguments == nil {
		return nil, "", "invalid_arguments"
	}
	if s.plan == nil || s.plan.Input == nil {
		return nil, "", "arguments_differ_from_plan"
	}
	var planned map[string]any
	if json.Unmarshal([]byte(*s.plan.Input), &planned) != nil || !reflect.DeepEqual(planned, a.Arguments) {
		return nil, "", "arguments_differ_from_plan"
	}
	out := map[string]any{"arguments": a.Arguments}
	if a.Path != nil && *a.Path != "" {
		if s.plan.Path == nil || *s.plan.Path != *a.Path {
			return nil, "", "arguments_differ_from_plan"
		}
		out["path"] = *a.Path
	}
	return out, c.ToolID, ""
}

// l2Docs describes active L2 tools for the planner.
func (e *Engine) l2Docs(ctx context.Context, toolIDs []string) map[string]string {
	out := map[string]string{}
	for _, id := range toolIDs {
		if !strings.HasPrefix(id, "l2.") {
			continue
		}
		if t, err := e.ActiveL2(ctx, id); err == nil {
			out[id] = t.Description + " Input JSON schema: " + truncate(string(t.InputSchema), 1500)
		}
	}
	return out
}
