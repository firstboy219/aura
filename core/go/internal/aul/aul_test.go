package aul

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/aul/ir"
	"aura.local/core/internal/aul/vm"
)

const digest = `
# Fetch pages and write a digest.
workflow page_digest version 1
requires web.fetch domains ["example.com"]
requires workspace.write paths ["reports/"]
input urls: list
input title: string

let parts = []
for u in urls {
  let page = call web.fetch { url: u }
  let parts = parts + [page.title + ": " + slice(page.text, 0, 20)]
}
if len(parts) == 0 { fail "no pages" }
call workspace.write { path: "reports/digest.md", content: "# " + title + "\n" + join(parts, "\n") }
return { pages: len(parts), first: parts[0] }
`

// D.1 P8: undeclared capabilities and out-of-scope literals fail static checks.
func TestCompileChecksCapabilities(t *testing.T) {
	p, diags := Compile(digest)
	if diags != nil || p.Name != "page_digest" || len(p.Requires) != 2 {
		t.Fatalf("valid workflow refused: %v", diags)
	}
	for _, c := range []struct{ name, src, want string }{
		{"undeclared tool", "workflow w version 1\nrequires web.fetch domains [\"example.com\"]\ncall workspace.write { path: \"reports/a.md\", content: \"x\" }", "undeclared capability"},
		{"literal url outside domains", "workflow w version 1\nrequires web.fetch domains [\"example.com\"]\nlet p = call web.fetch { url: \"https://evil.test/\" }", "outside the declared domains"},
		{"path outside", "workflow w version 1\nrequires workspace.write paths [\"reports/\"]\ncall workspace.write { path: \"secrets/a\", content: \"x\" }", "outside the declared paths"},
		{"traversal", "workflow w version 1\nrequires workspace.write paths [\"reports/\"]\ncall workspace.write { path: \"reports/../../etc/x\", content: \"x\" }", "outside the declared paths"},
		{"published needs approval", "workflow w version 1\nrequires workspace.write paths [\"published/\"]", "published/"},
		{"tool not offered", "workflow w version 1\nrequires browser.click\n", "not available to AUL"},
		{"shell-like tool", "workflow w version 1\nrequires system.exec paths [\"/\"]\n", "not available to AUL"},
		{"wrong args", "workflow w version 1\nrequires web.fetch domains [\"example.com\"]\nlet p = call web.fetch { url: \"https://example.com/\", method: \"POST\" }", "takes exactly"},
		{"undefined variable", "workflow w version 1\nreturn missing", "undefined variable"},
		{"unknown function", "workflow w version 1\nreturn eval(\"1\")", "unknown function"},
		{"loop var leaks", "workflow w version 1\nfor x in [1] { let y = x }\nreturn y", "undefined variable y"},
		{"bad domain", "workflow w version 1\nrequires web.fetch domains [\"127.0.0.1\"]", "bad domain"},
	} {
		_, diags := Compile(c.src)
		if !strings.Contains(fmt.Sprint(diags), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, diags)
		}
	}
}

// D.1 P8: malformed AUL never crashes the compiler (it reports a diagnostic).
func TestMalformedSourceNeverPanics(t *testing.T) {
	seeds := []string{digest, "workflow", "workflow w version", "workflow w version 1\nlet = ", "{{{{", "\"unterminated",
		"workflow w version 1\nfor x in { }", strings.Repeat("(", 100000), "workflow w version 1\nreturn " + strings.Repeat("[", 50) + strings.Repeat("]", 50)}
	r := rand.New(rand.NewSource(1))
	alphabet := []byte("workflow version requires let for in if else call return fail {}[]():,.+-*/<>=\"# \n abc 123 web.fetch")
	for i := 0; i < 3000; i++ {
		var b []byte
		if i < len(seeds) {
			b = []byte(seeds[i])
		} else {
			base := []byte(seeds[r.Intn(len(seeds))])
			for k := 0; k < 1+r.Intn(8) && len(base) > 0; k++ {
				pos := r.Intn(len(base))
				base[pos] = alphabet[r.Intn(len(alphabet))]
			}
			b = base
		}
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("compiler panicked on %q: %v", b, p)
				}
			}()
			p, diags := Compile(string(b))
			if p == nil && len(diags) == 0 {
				t.Fatalf("no program and no diagnostic for %q", b)
			}
		}()
	}
	if _, d := Compile(strings.Repeat("(", 100000)); !strings.Contains(fmt.Sprint(d), "") || d == nil {
		t.Fatal("deep nesting accepted")
	}
}

type fakeHost struct {
	mu    sync.Mutex
	calls []string
	files map[string]string
}

func (f *fakeHost) Call(_ context.Context, tool string, args map[string]any) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool)
	switch tool {
	case "web.fetch":
		u := args["url"].(string)
		if !URLAllowed(u, []string{"example.com"}) {
			return nil, errors.New("domain_not_allowed")
		}
		return map[string]any{"url": u, "status": 200.0, "title": "Example", "text": "This domain is for use in examples."}, nil
	case "workspace.write":
		if f.files == nil {
			f.files = map[string]string{}
		}
		f.files[args["path"].(string)] = args["content"].(string)
		return map[string]any{"path": args["path"], "bytes": float64(len(args["content"].(string)))}, nil
	}
	return nil, errors.New("tool_not_granted")
}

type syncHost struct{ h *fakeHost }

func (s syncHost) Call(tool string, args map[string]any) (any, error) {
	return s.h.Call(context.Background(), tool, args)
}

func TestVMSemanticsAndLimits(t *testing.T) {
	p, diags := Compile(digest)
	if diags != nil {
		t.Fatal(diags)
	}
	h := &fakeHost{}
	out, err := vm.Run(p, map[string]any{"urls": []any{"https://example.com/a", "https://example.com/b"}, "title": "Digest"}, syncHost{h}, 100000)
	if err != nil {
		t.Fatal(err)
	}
	o := out.(map[string]any)
	if o["pages"] != 2.0 || o["first"] != "Example: This domain is for u" || !strings.HasPrefix(h.files["reports/digest.md"], "# Digest\nExample") {
		t.Fatalf("output %v files %v", out, h.files)
	}
	if _, err := vm.Run(p, map[string]any{"urls": "x", "title": "t"}, syncHost{h}, 1000); err == nil {
		t.Fatal("an input of the wrong type was accepted")
	}
	loop, _ := Compile("workflow w version 1\nlet n = 0\nfor a in lines(\"" + strings.Repeat("x\\n", 2000) + "\") { for b in [1,2,3,4,5,6,7,8,9,10] { let n = n + 1 } }\nreturn n")
	if _, err := vm.Run(loop, nil, syncHost{h}, 5000); err == nil || !strings.Contains(err.Error(), "operation limit") {
		t.Fatalf("op limit: %v", err)
	}
	grow, _ := Compile("workflow w version 1\nlet s = \"xxxxxxxxxxxxxxxx\"\nfor i in lines(\"" + strings.Repeat("\\n", 30) + "\") { let s = s + s }\nreturn len(s)")
	if _, err := vm.Run(grow, nil, syncHost{h}, 100000); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("memory growth not bounded: %v", err)
	}
	// A tampered IR that calls an undeclared tool is refused by the VM too.
	bad := &ir.Program{IRVersion: ir.Version, Name: "x", Version: 1, Body: []*ir.Stmt{{Op: "expr", Expr: &ir.Expr{Op: "call", Name: "workspace.write",
		Args: []*ir.Expr{{Op: "obj", Keys: []string{"path", "content"}, Args: []*ir.Expr{{Op: "lit", Value: "a"}, {Op: "lit", Value: "b"}}}}}}}}
	if _, err := vm.Run(bad, nil, syncHost{h}, 100); err == nil || !strings.Contains(err.Error(), "undeclared capability") {
		t.Fatalf("tampered IR: %v", err)
	}
}

var (
	wasmOnce sync.Once
	wasmPath string
	wasmErr  error
)

// buildVM compiles cmd/aulvm to WebAssembly once per test run.
func buildVM(t *testing.T) string {
	t.Helper()
	wasmOnce.Do(func() {
		dir, err := os.MkdirTemp("", "aulvm")
		if err != nil {
			wasmErr = err
			return
		}
		wasmPath = filepath.Join(dir, "aulvm.wasm")
		cmd := exec.Command("go", "build", "-trimpath", "-o", wasmPath, "aura.local/core/cmd/aulvm")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			wasmErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if wasmErr != nil {
		t.Fatalf("building the AUL VM for wasip1: %v", wasmErr)
	}
	return wasmPath
}

// D.1 P8: AUL executes as WASM; its only way out is the host's tool calls.
func TestRunsAsWASM(t *testing.T) {
	ctx := context.Background()
	rt, err := NewRuntime(ctx, buildVM(t), "", Limits{Timeout: 20 * time.Second, MaxCalls: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close(ctx)
	p, _ := Compile(digest)
	h := &fakeHost{}
	res, err := rt.Run(ctx, p, map[string]any{"urls": []any{"https://example.com/a"}, "title": "T"}, h)
	if err != nil {
		t.Fatal(err)
	}
	if res.Calls != 2 || res.Output.(map[string]any)["pages"] != 1.0 || h.files["reports/digest.md"] == "" {
		t.Fatalf("result %+v calls %v", res, h.calls)
	}
	// A refused call fails the workflow (the host's answer is final).
	res, err = rt.Run(ctx, p, map[string]any{"urls": []any{"https://evil.test/"}, "title": "T"}, h)
	if !errors.Is(err, ErrWorkflow) || !strings.Contains(err.Error(), "domain_not_allowed") {
		t.Fatalf("refused call: %v", err)
	}
	// Call limit.
	many := make([]any, 10)
	for i := range many {
		many[i] = "https://example.com/" + fmt.Sprint(i)
	}
	if _, err := rt.Run(ctx, p, map[string]any{"urls": many, "title": "T"}, h); !errors.Is(err, ErrTooMany) {
		t.Fatalf("call limit: %v", err)
	}
	// Op limit inside WASM.
	spin, _ := Compile("workflow w version 1\nlet n = 0\nfor a in lines(\"" + strings.Repeat("\\n", 5000) + "\") { for b in lines(\"" + strings.Repeat("\\n", 5000) + "\") { let n = n + 1 } }\nreturn n")
	if _, err := rt.Run(ctx, spin, nil, h); !errors.Is(err, ErrWorkflow) || !strings.Contains(err.Error(), "operation limit") {
		t.Fatalf("op limit in wasm: %v", err)
	}
	// Timeout closes the module.
	slow, err := NewRuntime(ctx, buildVM(t), "", Limits{Timeout: 300 * time.Millisecond, MaxOps: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close(ctx)
	t0 := time.Now()
	if _, err := slow.Run(ctx, spin, nil, h); !errors.Is(err, ErrTimeout) || time.Since(t0) > 10*time.Second {
		t.Fatalf("timeout: %v after %s", err, time.Since(t0))
	}
}
