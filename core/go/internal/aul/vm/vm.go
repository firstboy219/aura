// Package vm interprets checked AUL IR (ADR-0050). It runs inside the AUL
// WebAssembly module (cmd/aulvm); the only way out is Host.Call, which the Core
// answers after checking the task's grant. It is deterministic and bounded:
// every evaluation step counts against MaxOps, and values have size limits.
package vm

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"aura.local/core/internal/aul/ir"
)

// Host performs a tool call on the workflow's behalf.
type Host interface {
	Call(tool string, args map[string]any) (any, error)
}

const (
	maxString = 1 << 20
	maxList   = 10000
)

// Error is a workflow failure with its source line.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

type returned struct{ v any }

func (returned) Error() string { return "return" }

type scope struct {
	vars   map[string]any
	parent *scope
}

func (s *scope) lookup(n string) (*scope, bool) {
	for c := s; c != nil; c = c.parent {
		if _, ok := c.vars[n]; ok {
			return c, true
		}
	}
	return nil, false
}

type machine struct {
	host   Host
	ops    int
	maxOps int
	tools  map[string]bool
}

// Run executes the program with its inputs.
func Run(p *ir.Program, inputs map[string]any, host Host, maxOps int) (any, error) {
	if p == nil || p.IRVersion != ir.Version {
		return nil, errors.New("unsupported IR version")
	}
	m := &machine{host: host, maxOps: maxOps, tools: map[string]bool{}}
	for _, r := range p.Requires {
		m.tools[r.Tool] = true
	}
	root := &scope{vars: map[string]any{}}
	for _, in := range p.Inputs {
		v, ok := inputs[in.Name]
		if !ok {
			return nil, &Error{0, "missing input " + in.Name}
		}
		if !hasType(v, in.Type) {
			return nil, &Error{0, fmt.Sprintf("input %s must be %s", in.Name, in.Type)}
		}
		root.vars[in.Name] = v
	}
	err := m.block(p.Body, root)
	var r returned
	if errors.As(err, &r) {
		return r.v, nil
	}
	return nil, err
}

func hasType(v any, t string) bool {
	switch t {
	case "any":
		return true
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	case "list":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}

func (m *machine) tick(line int) error {
	m.ops++
	if m.ops > m.maxOps {
		return &Error{line, fmt.Sprintf("operation limit (%d) reached", m.maxOps)}
	}
	return nil
}

func (m *machine) block(stmts []*ir.Stmt, sc *scope) error {
	for _, s := range stmts {
		if err := m.stmt(s, sc); err != nil {
			return err
		}
	}
	return nil
}

func (m *machine) stmt(s *ir.Stmt, sc *scope) error {
	if err := m.tick(s.Line); err != nil {
		return err
	}
	switch s.Op {
	case "let":
		v, err := m.eval(s.Expr, sc)
		if err != nil {
			return err
		}
		if owner, ok := sc.lookup(s.Name); ok {
			owner.vars[s.Name] = v
		} else {
			sc.vars[s.Name] = v
		}
		return nil
	case "expr":
		_, err := m.eval(s.Expr, sc)
		return err
	case "return":
		v, err := m.eval(s.Expr, sc)
		if err != nil {
			return err
		}
		return returned{v}
	case "fail":
		v, err := m.eval(s.Expr, sc)
		if err != nil {
			return err
		}
		return &Error{s.Line, "fail: " + toStr(v)}
	case "if":
		v, err := m.eval(s.Expr, sc)
		if err != nil {
			return err
		}
		b, ok := v.(bool)
		if !ok {
			return &Error{s.Line, "if condition must be a bool"}
		}
		if b {
			return m.block(s.Body, &scope{vars: map[string]any{}, parent: sc})
		}
		return m.block(s.Else, &scope{vars: map[string]any{}, parent: sc})
	case "for":
		v, err := m.eval(s.Expr, sc)
		if err != nil {
			return err
		}
		list, ok := v.([]any)
		if !ok {
			return &Error{s.Line, "for needs a list"}
		}
		for _, item := range list {
			inner := &scope{vars: map[string]any{s.Name: item}, parent: sc}
			if err := m.block(s.Body, inner); err != nil {
				return err
			}
		}
		return nil
	}
	return &Error{s.Line, "unknown statement " + s.Op}
}

func (m *machine) eval(e *ir.Expr, sc *scope) (any, error) {
	if e == nil {
		return nil, &Error{0, "missing expression"}
	}
	if err := m.tick(e.Line); err != nil {
		return nil, err
	}
	fail := func(f string, a ...any) (any, error) { return nil, &Error{e.Line, fmt.Sprintf(f, a...)} }
	args := func() ([]any, error) {
		out := make([]any, len(e.Args))
		for i, a := range e.Args {
			v, err := m.eval(a, sc)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	switch e.Op {
	case "lit":
		return e.Value, nil
	case "var":
		owner, ok := sc.lookup(e.Name)
		if !ok {
			return fail("undefined variable %s", e.Name)
		}
		return owner.vars[e.Name], nil
	case "list":
		vs, err := args()
		if err != nil {
			return nil, err
		}
		if len(vs) > maxList {
			return fail("list too long")
		}
		return vs, nil
	case "obj":
		vs, err := args()
		if err != nil {
			return nil, err
		}
		if len(vs) != len(e.Keys) {
			return fail("malformed object")
		}
		o := map[string]any{}
		for i, k := range e.Keys {
			o[k] = vs[i]
		}
		return o, nil
	case "field":
		vs, err := args()
		if err != nil || len(vs) != 1 {
			return nil, orErr(err, e.Line, "malformed field access")
		}
		o, ok := vs[0].(map[string]any)
		if !ok {
			return fail("%s: not an object", e.Name)
		}
		v, ok := o[e.Name]
		if !ok {
			return fail("no field %s", e.Name)
		}
		return v, nil
	case "index":
		vs, err := args()
		if err != nil || len(vs) != 2 {
			return nil, orErr(err, e.Line, "malformed index")
		}
		switch c := vs[0].(type) {
		case []any:
			i, ok := intOf(vs[1])
			if !ok || i < 0 || i >= len(c) {
				return fail("index out of range")
			}
			return c[i], nil
		case map[string]any:
			k, ok := vs[1].(string)
			if !ok {
				return fail("object index must be a string")
			}
			v, ok := c[k]
			if !ok {
				return fail("no field %s", k)
			}
			return v, nil
		}
		return fail("cannot index this value")
	case "not":
		vs, err := args()
		if err != nil || len(vs) != 1 {
			return nil, orErr(err, e.Line, "malformed not")
		}
		b, ok := vs[0].(bool)
		if !ok {
			return fail("not needs a bool")
		}
		return !b, nil
	case "neg":
		vs, err := args()
		if err != nil || len(vs) != 1 {
			return nil, orErr(err, e.Line, "malformed negation")
		}
		f, ok := vs[0].(float64)
		if !ok {
			return fail("- needs a number")
		}
		return -f, nil
	case "bin":
		return m.binary(e, sc)
	case "func":
		vs, err := args()
		if err != nil {
			return nil, err
		}
		return builtin(e, vs)
	case "call":
		if !m.tools[e.Name] {
			return fail("undeclared capability %s", e.Name) // the checker refuses this; the Core checks again
		}
		vs, err := args()
		if err != nil || len(vs) != 1 {
			return nil, orErr(err, e.Line, "malformed call")
		}
		a, ok := vs[0].(map[string]any)
		if !ok {
			return fail("call arguments must be an object")
		}
		v, err := m.host.Call(e.Name, a)
		if err != nil {
			return fail("%s: %v", e.Name, err)
		}
		return v, nil
	}
	return fail("unknown expression %s", e.Op)
}

func orErr(err error, line int, msg string) error {
	if err != nil {
		return err
	}
	return &Error{line, msg}
}

func (m *machine) binary(e *ir.Expr, sc *scope) (any, error) {
	if len(e.Args) != 2 {
		return nil, &Error{e.Line, "malformed operator"}
	}
	a, err := m.eval(e.Args[0], sc)
	if err != nil {
		return nil, err
	}
	if e.Name == "and" || e.Name == "or" { // short-circuit
		ab, ok := a.(bool)
		if !ok {
			return nil, &Error{e.Line, e.Name + " needs bools"}
		}
		if (e.Name == "and" && !ab) || (e.Name == "or" && ab) {
			return ab, nil
		}
		b, err := m.eval(e.Args[1], sc)
		if err != nil {
			return nil, err
		}
		bb, ok := b.(bool)
		if !ok {
			return nil, &Error{e.Line, e.Name + " needs bools"}
		}
		return bb, nil
	}
	b, err := m.eval(e.Args[1], sc)
	if err != nil {
		return nil, err
	}
	fail := func(f string, x ...any) (any, error) { return nil, &Error{e.Line, fmt.Sprintf(f, x...)} }
	switch e.Name {
	case "==":
		return equal(a, b), nil
	case "!=":
		return !equal(a, b), nil
	case "+":
		switch x := a.(type) {
		case float64:
			if y, ok := b.(float64); ok {
				return x + y, nil
			}
		case string:
			if y, ok := b.(string); ok {
				if len(x)+len(y) > maxString {
					return fail("string too long")
				}
				return x + y, nil
			}
		case []any:
			if y, ok := b.([]any); ok {
				if len(x)+len(y) > maxList {
					return fail("list too long")
				}
				return append(append([]any{}, x...), y...), nil
			}
		}
		return fail("+ needs two numbers, two strings or two lists")
	case "-", "*", "/", "<", ">", "<=", ">=":
		if xs, ok := a.(string); ok && e.Name != "-" && e.Name != "*" && e.Name != "/" {
			ys, ok := b.(string)
			if !ok {
				return fail("%s compares two strings or two numbers", e.Name)
			}
			switch e.Name {
			case "<":
				return xs < ys, nil
			case ">":
				return xs > ys, nil
			case "<=":
				return xs <= ys, nil
			}
			return xs >= ys, nil
		}
		x, ok1 := a.(float64)
		y, ok2 := b.(float64)
		if !ok1 || !ok2 {
			return fail("%s needs numbers", e.Name)
		}
		switch e.Name {
		case "-":
			return x - y, nil
		case "*":
			return x * y, nil
		case "/":
			if y == 0 {
				return fail("division by zero")
			}
			return x / y, nil
		case "<":
			return x < y, nil
		case ">":
			return x > y, nil
		case "<=":
			return x <= y, nil
		}
		return x >= y, nil
	}
	return fail("unknown operator %s", e.Name)
}

func equal(a, b any) bool {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || !equal(v, w) {
				return false
			}
		}
		return true
	}
	return a == b
}

func intOf(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.Abs(f) > 1e9 {
		return 0, false
	}
	return int(f), true
}

func toStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return fmt.Sprint(v)
}

func builtin(e *ir.Expr, a []any) (any, error) {
	fail := func(f string, x ...any) (any, error) {
		return nil, &Error{e.Line, e.Name + ": " + fmt.Sprintf(f, x...)}
	}
	ar, ok := ir.Builtins[e.Name]
	if !ok || len(a) < ar[0] || len(a) > ar[1] {
		return fail("unknown function or wrong number of arguments")
	}
	str := func(i int) (string, bool) { s, ok := a[i].(string); return s, ok }
	switch e.Name {
	case "len":
		switch x := a[0].(type) {
		case string:
			return float64(len([]rune(x))), nil
		case []any:
			return float64(len(x)), nil
		case map[string]any:
			return float64(len(x)), nil
		}
		return fail("needs a string, list or object")
	case "str":
		return toStr(a[0]), nil
	case "num":
		switch x := a[0].(type) {
		case float64:
			return x, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return fail("not a number: %q", truncate(x, 40))
			}
			return f, nil
		}
		return fail("needs a string or number")
	case "join":
		l, ok := a[0].([]any)
		sep, ok2 := str(1)
		if !ok || !ok2 {
			return fail("join(list, separator)")
		}
		parts := make([]string, len(l))
		n := 0
		for i, v := range l {
			parts[i] = toStr(v)
			n += len(parts[i]) + len(sep)
		}
		if n > maxString {
			return fail("result too long")
		}
		return strings.Join(parts, sep), nil
	case "lines", "split":
		s, ok := str(0)
		sep := "\n"
		if e.Name == "split" {
			var ok2 bool
			sep, ok2 = str(1)
			ok = ok && ok2 && sep != ""
		}
		if !ok {
			return fail("needs strings")
		}
		ps := strings.Split(s, sep)
		if len(ps) > maxList {
			return fail("too many parts")
		}
		out := make([]any, len(ps))
		for i, p := range ps {
			out[i] = p
		}
		return out, nil
	case "slice":
		i, ok1 := intOf(a[1])
		j, ok2 := intOf(a[2])
		if !ok1 || !ok2 {
			return fail("indexes must be whole numbers")
		}
		switch x := a[0].(type) {
		case string:
			r := []rune(x)
			i, j = clamp(i, len(r)), clamp(j, len(r))
			if i > j {
				i = j
			}
			return string(r[i:j]), nil
		case []any:
			i, j = clamp(i, len(x)), clamp(j, len(x))
			if i > j {
				i = j
			}
			return append([]any{}, x[i:j]...), nil
		}
		return fail("needs a string or list")
	case "contains":
		switch x := a[0].(type) {
		case string:
			sub, ok := str(1)
			if !ok {
				return fail("contains(string, string)")
			}
			return strings.Contains(x, sub), nil
		case []any:
			for _, v := range x {
				if equal(v, a[1]) {
					return true, nil
				}
			}
			return false, nil
		}
		return fail("needs a string or list")
	case "lower", "upper", "trim":
		s, ok := str(0)
		if !ok {
			return fail("needs a string")
		}
		switch e.Name {
		case "lower":
			return strings.ToLower(s), nil
		case "upper":
			return strings.ToUpper(s), nil
		}
		return strings.TrimSpace(s), nil
	case "replace":
		s, ok1 := str(0)
		o, ok2 := str(1)
		n, ok3 := str(2)
		if !ok1 || !ok2 || !ok3 || o == "" {
			return fail("replace(string, old, new)")
		}
		if c := strings.Count(s, o); len(s)+c*(len(n)-len(o)) > maxString {
			return fail("result too long")
		}
		return strings.ReplaceAll(s, o, n), nil
	case "keys":
		o, ok := a[0].(map[string]any)
		if !ok {
			return fail("needs an object")
		}
		ks := make([]string, 0, len(o))
		for k := range o {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		out := make([]any, len(ks))
		for i, k := range ks {
			out[i] = k
		}
		return out, nil
	}
	return fail("unknown function")
}

func clamp(i, n int) int {
	if i < 0 {
		i += n
	}
	return max(0, min(i, n))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
