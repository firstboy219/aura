// Package aul compiles AURA Language workflows (Blueprint §11 module 11, D.1
// P8, ADR-0050): Source → Lexer → Parser → AST → type/capability check → IR.
// The IR runs in the AUL VM inside WebAssembly (run.go); the Core stays the
// final authority for every tool call.
//
// AUL is small and declarative on purpose: no loops except over lists, no
// recursion, no functions, no network, file, process or clock access. A
// workflow declares the capabilities it needs up front; a call to a tool that
// is not declared, or a literal URL/path outside what was declared, fails the
// check before anything runs.
package aul

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"aura.local/core/internal/aul/ir"
)

// Limits on what the compiler accepts.
const (
	MaxSource = 64 << 10
	maxDepth  = 16
	maxNodes  = 5000
)

// Diagnostic is one compile error.
type Diagnostic struct {
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Msg  string `json:"message"`
}

func (d Diagnostic) String() string { return fmt.Sprintf("%d:%d: %s", d.Line, d.Col, d.Msg) }

// ---------------------------------------------------------------- lexer

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tString
	tNumber
	tPunct
)

type token struct {
	kind      tokKind
	text      string
	line, col int
}

var keywords = map[string]bool{"workflow": true, "version": true, "requires": true, "domains": true, "paths": true, "input": true,
	"let": true, "for": true, "in": true, "if": true, "else": true, "call": true, "return": true, "fail": true, "true": true,
	"false": true, "null": true, "and": true, "or": true, "not": true}

func lex(src string) ([]token, *Diagnostic) {
	var out []token
	line, col := 1, 1
	rs := []rune(src)
	for i := 0; i < len(rs); {
		c := rs[i]
		adv := func(n int) {
			for k := 0; k < n; k++ {
				if rs[i] == '\n' {
					line, col = line+1, 1
				} else {
					col++
				}
				i++
			}
		}
		switch {
		case c == '#':
			for i < len(rs) && rs[i] != '\n' {
				adv(1)
			}
		case unicode.IsSpace(c):
			adv(1)
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			out = append(out, token{tIdent, string(rs[i:j]), line, col})
			adv(j - i)
		case unicode.IsDigit(c):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.') {
				j++
			}
			out = append(out, token{tNumber, string(rs[i:j]), line, col})
			adv(j - i)
		case c == '"':
			var b strings.Builder
			l0, c0 := line, col
			adv(1)
			closed := false
			for i < len(rs) {
				if rs[i] == '"' {
					adv(1)
					closed = true
					break
				}
				if rs[i] == '\n' {
					break
				}
				if rs[i] == '\\' && i+1 < len(rs) {
					switch rs[i+1] {
					case 'n':
						b.WriteRune('\n')
					case 't':
						b.WriteRune('\t')
					case '"':
						b.WriteRune('"')
					case '\\':
						b.WriteRune('\\')
					default:
						return nil, &Diagnostic{line, col, "unknown escape \\" + string(rs[i+1])}
					}
					adv(2)
					continue
				}
				b.WriteRune(rs[i])
				adv(1)
			}
			if !closed {
				return nil, &Diagnostic{l0, c0, "unterminated string"}
			}
			out = append(out, token{tString, b.String(), l0, c0})
		default:
			two := ""
			if i+1 < len(rs) {
				two = string(rs[i : i+2])
			}
			if two == "==" || two == "!=" || two == "<=" || two == ">=" {
				out = append(out, token{tPunct, two, line, col})
				adv(2)
				continue
			}
			if strings.ContainsRune("{}[]():,.+-*/<>=", c) {
				out = append(out, token{tPunct, string(c), line, col})
				adv(1)
				continue
			}
			return nil, &Diagnostic{line, col, fmt.Sprintf("unexpected character %q", c)}
		}
	}
	return append(out, token{tEOF, "", line, col}), nil
}

// ---------------------------------------------------------------- parser

type parser struct {
	toks  []token
	pos   int
	nodes int
	depth int
	err   *Diagnostic
}

type bail struct{}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) fail(t token, f string, a ...any) {
	if p.err == nil {
		p.err = &Diagnostic{t.line, t.col, fmt.Sprintf(f, a...)}
	}
	panic(bail{})
}

func (p *parser) is(text string) bool {
	t := p.peek()
	return (t.kind == tPunct || t.kind == tIdent) && t.text == text
}

func (p *parser) expect(text string) token {
	t := p.next()
	if (t.kind != tPunct && t.kind != tIdent) || t.text != text {
		p.fail(t, "expected %q, found %s", text, describe(t))
	}
	return t
}

func describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of file"
	case tString:
		return "a string"
	}
	return strconv.Quote(t.text)
}

func (p *parser) ident() token {
	t := p.next()
	if t.kind != tIdent || keywords[t.text] {
		p.fail(t, "expected a name, found %s", describe(t))
	}
	return t
}

// toolName reads a dotted tool name such as web.fetch.
func (p *parser) toolName() token {
	t := p.ident()
	for p.is(".") {
		p.next()
		t.text += "." + p.ident().text
	}
	return t
}

func (p *parser) count() {
	p.nodes++
	if p.nodes > maxNodes {
		p.fail(p.peek(), "workflow too large (more than %d nodes)", maxNodes)
	}
}

func (p *parser) enter() {
	p.depth++
	if p.depth > maxDepth {
		p.fail(p.peek(), "nesting deeper than %d", maxDepth)
	}
}

func (p *parser) leave() { p.depth-- }

func (p *parser) program() *ir.Program {
	prog := &ir.Program{IRVersion: ir.Version}
	p.expect("workflow")
	prog.Name = p.ident().text
	p.expect("version")
	vt := p.next()
	v, err := strconv.Atoi(vt.text)
	if vt.kind != tNumber || err != nil || v < 1 || v > 1000000 {
		p.fail(vt, "version must be a whole number from 1")
	}
	prog.Version = v
	for p.is("requires") {
		p.next()
		r := ir.Require{Tool: p.toolName().text}
		switch {
		case p.is("domains"):
			p.next()
			r.Domains = p.stringList()
		case p.is("paths"):
			p.next()
			r.Paths = p.stringList()
		}
		prog.Requires = append(prog.Requires, r)
	}
	for p.is("input") {
		p.next()
		in := ir.Input{Name: p.ident().text}
		p.expect(":")
		in.Type = p.ident().text
		prog.Inputs = append(prog.Inputs, in)
	}
	for p.peek().kind != tEOF {
		prog.Body = append(prog.Body, p.stmt())
	}
	return prog
}

func (p *parser) stringList() []string {
	p.expect("[")
	var out []string
	for !p.is("]") {
		t := p.next()
		if t.kind != tString {
			p.fail(t, "expected a string, found %s", describe(t))
		}
		out = append(out, t.text)
		if !p.is("]") {
			p.expect(",")
		}
	}
	p.expect("]")
	return out
}

func (p *parser) block() []*ir.Stmt {
	p.expect("{")
	p.enter()
	defer p.leave()
	var out []*ir.Stmt
	for !p.is("}") {
		if p.peek().kind == tEOF {
			p.fail(p.peek(), "missing }")
		}
		out = append(out, p.stmt())
	}
	p.expect("}")
	return out
}

func (p *parser) stmt() *ir.Stmt {
	p.count()
	t := p.peek()
	s := &ir.Stmt{Line: t.line}
	switch {
	case p.is("let"):
		p.next()
		s.Op, s.Name = "let", p.ident().text
		p.expect("=")
		s.Expr = p.expr()
	case p.is("for"):
		p.next()
		s.Op, s.Name = "for", p.ident().text
		p.expect("in")
		s.Expr = p.expr()
		s.Body = p.block()
	case p.is("if"):
		p.next()
		s.Op, s.Expr = "if", p.expr()
		s.Body = p.block()
		if p.is("else") {
			p.next()
			if p.is("if") {
				s.Else = []*ir.Stmt{p.stmt()}
			} else {
				s.Else = p.block()
			}
		}
	case p.is("return"):
		p.next()
		s.Op, s.Expr = "return", p.expr()
	case p.is("fail"):
		p.next()
		s.Op, s.Expr = "fail", p.expr()
	case p.is("call"):
		s.Op, s.Expr = "expr", p.expr()
	default:
		p.fail(t, "expected a statement (let, for, if, call, return, fail), found %s", describe(t))
	}
	return s
}

var precedence = map[string]int{"or": 1, "and": 2, "==": 3, "!=": 3, "<": 3, ">": 3, "<=": 3, ">=": 3, "+": 4, "-": 4, "*": 5, "/": 5}

func (p *parser) expr() *ir.Expr { return p.binary(1) }

func (p *parser) binary(min int) *ir.Expr {
	left := p.unary()
	for {
		t := p.peek()
		prec, ok := precedence[t.text]
		if !ok || (t.kind != tPunct && t.kind != tIdent) || prec < min {
			return left
		}
		p.next()
		p.count()
		right := p.binary(prec + 1)
		left = &ir.Expr{Op: "bin", Name: t.text, Args: []*ir.Expr{left, right}, Line: t.line}
	}
}

func (p *parser) unary() *ir.Expr {
	t := p.peek()
	if p.is("not") || p.is("-") {
		p.next()
		p.count()
		p.enter()
		defer p.leave()
		op := "not"
		if t.text == "-" {
			op = "neg"
		}
		return &ir.Expr{Op: op, Args: []*ir.Expr{p.unary()}, Line: t.line}
	}
	return p.postfix(p.primary())
}

func (p *parser) postfix(e *ir.Expr) *ir.Expr {
	for {
		t := p.peek()
		switch {
		case p.is("."):
			p.next()
			p.count()
			e = &ir.Expr{Op: "field", Name: p.ident().text, Args: []*ir.Expr{e}, Line: t.line}
		case p.is("["):
			p.next()
			p.count()
			idx := p.expr()
			p.expect("]")
			e = &ir.Expr{Op: "index", Args: []*ir.Expr{e, idx}, Line: t.line}
		default:
			return e
		}
	}
}

func (p *parser) primary() *ir.Expr {
	p.count()
	p.enter()
	defer p.leave()
	t := p.next()
	e := &ir.Expr{Line: t.line}
	switch {
	case t.kind == tString:
		e.Op, e.Value = "lit", t.text
	case t.kind == tNumber:
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			p.fail(t, "bad number %s", t.text)
		}
		e.Op, e.Value = "lit", f
	case t.kind == tIdent && (t.text == "true" || t.text == "false"):
		e.Op, e.Value = "lit", t.text == "true"
	case t.kind == tIdent && t.text == "null":
		e.Op = "lit"
	case t.kind == tIdent && t.text == "call":
		e.Op, e.Name = "call", p.toolName().text
		if !p.is("{") {
			p.fail(p.peek(), "call %s needs an argument object { ... }", e.Name)
		}
		e.Args = []*ir.Expr{p.object()}
	case t.kind == tPunct && t.text == "(":
		e = p.expr()
		p.expect(")")
	case t.kind == tPunct && t.text == "[":
		e.Op = "list"
		for !p.is("]") {
			e.Args = append(e.Args, p.expr())
			if !p.is("]") {
				p.expect(",")
			}
		}
		p.expect("]")
	case t.kind == tPunct && t.text == "{":
		p.pos--
		return p.object()
	case t.kind == tIdent && !keywords[t.text]:
		if p.is("(") {
			p.next()
			e.Op, e.Name = "func", t.text
			for !p.is(")") {
				e.Args = append(e.Args, p.expr())
				if !p.is(")") {
					p.expect(",")
				}
			}
			p.expect(")")
		} else {
			e.Op, e.Name = "var", t.text
		}
	default:
		p.fail(t, "expected a value, found %s", describe(t))
	}
	return e
}

func (p *parser) object() *ir.Expr {
	t := p.expect("{")
	e := &ir.Expr{Op: "obj", Line: t.line}
	for !p.is("}") {
		k := p.next()
		if k.kind != tIdent && k.kind != tString {
			p.fail(k, "expected a field name, found %s", describe(k))
		}
		if slices.Contains(e.Keys, k.text) {
			p.fail(k, "duplicate field %s", k.text)
		}
		p.expect(":")
		e.Keys = append(e.Keys, k.text)
		e.Args = append(e.Args, p.expr())
		if !p.is("}") {
			p.expect(",")
		}
	}
	p.expect("}")
	return e
}

// ---------------------------------------------------------------- checker

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	inTypes  = []string{"string", "number", "bool", "list", "object", "any"}
)

type checker struct {
	prog  *ir.Program
	diags []Diagnostic
	tools map[string]ir.Require
}

func (c *checker) errf(line int, f string, a ...any) {
	c.diags = append(c.diags, Diagnostic{Line: line, Msg: fmt.Sprintf(f, a...)})
}

func (c *checker) check() {
	p := c.prog
	if !nameRe.MatchString(p.Name) {
		c.errf(1, "workflow name must be lower_snake_case (1..41 chars)")
	}
	c.tools = map[string]ir.Require{}
	for _, r := range p.Requires {
		if _, dup := c.tools[r.Tool]; dup {
			c.errf(1, "%s is required twice", r.Tool)
			continue
		}
		c.tools[r.Tool] = r
		switch r.Tool {
		case "web.fetch":
			if len(r.Domains) == 0 || len(r.Paths) > 0 {
				c.errf(1, "requires web.fetch needs domains [...]")
			}
			for _, d := range r.Domains {
				if !domainRe.MatchString(d) {
					c.errf(1, "bad domain %q", d)
				}
			}
		case "workspace.read", "workspace.write":
			if len(r.Paths) == 0 || len(r.Domains) > 0 {
				c.errf(1, "requires %s needs paths [...]", r.Tool)
			}
			for _, pp := range r.Paths {
				if cleanPrefix(pp) == "" {
					c.errf(1, "bad path prefix %q (relative, no ..)", pp)
				}
				if r.Tool == "workspace.write" && (strings.HasPrefix(pp, "published") || pp == "" || pp == ".") {
					c.errf(1, "workspace.write may not declare %q: published/ needs an owner approval, which workflows cannot ask for", pp)
				}
			}
		default:
			c.errf(1, "%s is not available to AUL workflows (available: web.fetch, workspace.read, workspace.write)", r.Tool)
		}
	}
	seen := map[string]bool{}
	for _, in := range p.Inputs {
		if !nameRe.MatchString(in.Name) || seen[in.Name] {
			c.errf(1, "input name %q is invalid or repeated", in.Name)
		}
		if !slices.Contains(inTypes, in.Type) {
			c.errf(1, "input %s: type must be one of %s", in.Name, strings.Join(inTypes, ", "))
		}
		seen[in.Name] = true
	}
	root := []map[string]bool{seen}
	c.block(p.Body, root)
}

func defined(sc []map[string]bool, n string) bool {
	for _, s := range sc {
		if s[n] {
			return true
		}
	}
	return false
}

func (c *checker) block(stmts []*ir.Stmt, sc []map[string]bool) {
	for _, s := range stmts {
		switch s.Op {
		case "let":
			c.expr(s.Expr, sc)
			if !nameRe.MatchString(s.Name) {
				c.errf(s.Line, "variable name %q must be lower_snake_case", s.Name)
			}
			if !defined(sc, s.Name) {
				sc[len(sc)-1][s.Name] = true
			}
		case "for":
			c.expr(s.Expr, sc)
			if defined(sc, s.Name) {
				c.errf(s.Line, "loop variable %s hides an existing variable", s.Name)
			}
			c.block(s.Body, append(sc, map[string]bool{s.Name: true}))
		case "if":
			c.expr(s.Expr, sc)
			c.block(s.Body, append(sc, map[string]bool{}))
			c.block(s.Else, append(sc, map[string]bool{}))
		case "expr", "return", "fail":
			c.expr(s.Expr, sc)
		}
	}
}

func (c *checker) expr(e *ir.Expr, sc []map[string]bool) {
	if e == nil {
		return
	}
	switch e.Op {
	case "var":
		if !defined(sc, e.Name) {
			c.errf(e.Line, "undefined variable %s", e.Name)
		}
	case "func":
		ar, ok := ir.Builtins[e.Name]
		if !ok {
			c.errf(e.Line, "unknown function %s", e.Name)
		} else if len(e.Args) < ar[0] || len(e.Args) > ar[1] {
			c.errf(e.Line, "%s takes %d argument(s)", e.Name, ar[0])
		}
	case "call":
		r, ok := c.tools[e.Name]
		if !ok {
			c.errf(e.Line, "undeclared capability: %s is called but not in requires", e.Name)
			break
		}
		obj := e.Args[0]
		want := ir.ToolArgs[e.Name]
		if !slices.Equal(sortedCopy(obj.Keys), sortedCopy(want)) {
			c.errf(e.Line, "%s takes exactly { %s }", e.Name, strings.Join(want, ", "))
			break
		}
		for i, k := range obj.Keys {
			lit, isLit := obj.Args[i].Value.(string)
			if obj.Args[i].Op != "lit" || !isLit {
				continue // checked by the Core at run time
			}
			switch {
			case e.Name == "web.fetch" && k == "url":
				if !URLAllowed(lit, r.Domains) {
					c.errf(e.Line, "%s is outside the declared domains", lit)
				}
			case k == "path":
				if !PathAllowed(lit, r.Paths) {
					c.errf(e.Line, "%s is outside the declared paths", lit)
				}
			}
		}
	}
	for _, a := range e.Args {
		c.expr(a, sc)
	}
}

func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	slices.Sort(out)
	return out
}

func cleanPrefix(p string) string {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return ""
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return ""
	}
	return c
}

// PathAllowed reports whether a workspace path is inside one of the prefixes.
func PathAllowed(p string, prefixes []string) bool {
	c := cleanPrefix(p)
	if c == "" {
		return false
	}
	for _, pre := range prefixes {
		pc := cleanPrefix(pre)
		if pc != "" && (c == pc || strings.HasPrefix(c, pc+"/")) {
			return true
		}
	}
	return false
}

// URLAllowed reports whether an http(s) URL's host is a declared domain or a subdomain of one.
func URLAllowed(raw string, domains []string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, d := range domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// Compile turns source into checked IR, or returns diagnostics.
func Compile(src string) (*ir.Program, []Diagnostic) {
	if len(src) > MaxSource {
		return nil, []Diagnostic{{1, 1, fmt.Sprintf("source larger than %d bytes", MaxSource)}}
	}
	toks, d := lex(src)
	if d != nil {
		return nil, []Diagnostic{*d}
	}
	p := &parser{toks: toks}
	prog := func() (out *ir.Program) {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(bail); !ok {
					panic(r)
				}
				out = nil
			}
		}()
		return p.program()
	}()
	if p.err != nil {
		return nil, []Diagnostic{*p.err}
	}
	c := &checker{prog: prog}
	c.check()
	if len(c.diags) > 0 {
		return nil, c.diags
	}
	return prog, nil
}
