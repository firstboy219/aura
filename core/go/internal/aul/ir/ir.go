// Package ir is the checked intermediate representation of an AUL workflow
// (Blueprint §11 pipeline: Source → Lexer → Parser → AST → check → IR → WASM,
// ADR-0050). It is plain data, shared by the compiler (Core) and the VM that
// runs inside WebAssembly, so it has no dependencies.
package ir

// Version of the IR format; the VM refuses any other.
const Version = 1

// Program is a checked workflow.
type Program struct {
	IRVersion int       `json:"ir_version"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	Requires  []Require `json:"requires"`
	Inputs    []Input   `json:"inputs"`
	Body      []*Stmt   `json:"body"`
}

// Require is a declared capability: a tool and the domains or paths it may touch.
type Require struct {
	Tool    string   `json:"tool"`
	Domains []string `json:"domains,omitempty"`
	Paths   []string `json:"paths,omitempty"`
}

// Input is a declared workflow input.
type Input struct {
	Name string `json:"name"`
	Type string `json:"type"` // string | number | bool | list | object | any
}

// Stmt ops: let, for, if, expr, return, fail.
type Stmt struct {
	Op   string  `json:"op"`
	Name string  `json:"name,omitempty"` // let target, for variable
	Expr *Expr   `json:"expr,omitempty"`
	Body []*Stmt `json:"body,omitempty"`
	Else []*Stmt `json:"else,omitempty"`
	Line int     `json:"line"`
}

// Expr ops: lit, var, list, obj, field, index, bin, not, neg, func, call.
type Expr struct {
	Op    string   `json:"op"`
	Value any      `json:"value,omitempty"` // lit
	Name  string   `json:"name,omitempty"`  // var, field, bin operator, func, call tool
	Keys  []string `json:"keys,omitempty"`  // obj
	Args  []*Expr  `json:"args,omitempty"`
	Line  int      `json:"line"`
}

// Builtins and their arities (the checker and the VM share this table).
var Builtins = map[string][2]int{
	"len": {1, 1}, "join": {2, 2}, "str": {1, 1}, "num": {1, 1}, "lines": {1, 1}, "split": {2, 2}, "slice": {3, 3},
	"contains": {2, 2}, "lower": {1, 1}, "upper": {1, 1}, "trim": {1, 1}, "replace": {3, 3}, "keys": {1, 1},
}

// ToolArgs are the argument keys each AUL-callable tool takes (all required).
var ToolArgs = map[string][]string{
	"web.fetch":       {"url"},
	"workspace.read":  {"path"},
	"workspace.write": {"path", "content"},
}

// Message is one line of the VM ↔ host protocol over WASI stdin/stdout.
type Message struct {
	// host → VM, first line
	Program *Program       `json:"program,omitempty"`
	Inputs  map[string]any `json:"inputs,omitempty"`
	MaxOps  int            `json:"max_ops,omitempty"`
	// VM → host
	Call string         `json:"call,omitempty"`
	Args map[string]any `json:"args,omitempty"`
	Done bool           `json:"done,omitempty"`
	// host → VM reply to a call
	OK     bool `json:"ok,omitempty"`
	Result any  `json:"result,omitempty"`
	// both: an error (VM: the workflow failed; host: the call was refused or failed)
	Error  string `json:"error,omitempty"`
	Output any    `json:"output,omitempty"`
}
