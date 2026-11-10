package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aura.local/core/internal/aul"
	"aura.local/core/internal/task"
	"aura.local/core/internal/taxonomy"
)

var (
	aulOnce sync.Once
	aulWasm string
	aulErr  error
)

func withAUL(t *testing.T, h *harness) {
	t.Helper()
	aulOnce.Do(func() {
		dir, err := os.MkdirTemp("", "aulvm")
		if err != nil {
			aulErr = err
			return
		}
		aulWasm = filepath.Join(dir, "aulvm.wasm")
		cmd := exec.Command("go", "build", "-trimpath", "-o", aulWasm, "aura.local/core/cmd/aulvm")
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			aulErr = fmt.Errorf("%v: %s", err, out)
		}
	})
	if aulErr != nil {
		t.Fatalf("building the AUL VM: %v", aulErr)
	}
	rt, err := aul.NewRuntime(context.Background(), aulWasm, "", aul.Limits{Timeout: 30 * time.Second, MaxCalls: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close(context.Background()) })
	h.eng.aul = rt
}

func aulDigest(host string, version int) string {
	return fmt.Sprintf(`workflow solar_digest version %d
requires web.fetch domains ["%s"]
requires workspace.write paths ["reports/"]
input pages: list
let titles = []
for p in pages {
  let page = call web.fetch { url: p }
  let titles = titles + [page.title]
}
call workspace.write { path: "reports/digest.md", content: "Pages: " + join(titles, ", ") }
return { count: len(titles) }
`, version, host)
}

// D.1 P8: a registered AUL workflow runs as WASM under the Core's grant; a
// call outside the declared capabilities is refused by the Core at run time;
// tampered IR is refused; versions only go up.
func TestP8AULWorkflows(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	withAUL(t, h)
	ctx := context.Background()
	if _, diags, err := h.eng.RegisterWorkflow(ctx, "workflow x version 1\ncall workspace.write { path: \"a\", content: \"b\" }", "owner"); diags == nil || err == nil {
		t.Fatal("an undeclared capability was registered")
	}
	w, diags, err := h.eng.RegisterWorkflow(ctx, aulDigest(h.host, 1), "owner")
	if err != nil {
		t.Fatalf("%v %v", err, diags)
	}
	if _, _, err := h.eng.RegisterWorkflow(ctx, aulDigest(h.host, 1), "owner"); err != nil {
		t.Fatalf("identical re-registration: %v", err)
	}
	if _, _, err := h.eng.RegisterWorkflow(ctx, strings.Replace(aulDigest(h.host, 1), "Pages: ", "Seen: ", 1), "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed content under the same version: %v", err)
	}
	tk, err := h.eng.RunWorkflow(ctx, "solar_digest", map[string]any{"pages": []any{"http://" + h.host + "/one"}}, task.Budget{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tk.Scope.Tools, ",") != "web.fetch,workspace.write" || tk.Scope.Workflow != w.ID || tk.AgentRole != "workflow" {
		t.Fatalf("grant %v", tk.Scope)
	}
	done := h.wait(tk.ID, task.Succeeded, task.Failed)
	if done.State != task.Succeeded {
		t.Fatalf("workflow task: %s %s", done.State, errorDetail(h.events(tk.ID)))
	}
	f, err := h.ws.Read(tk.ID, "reports/digest.md")
	if err != nil || f.Content != "Pages: Solar" {
		t.Fatalf("output file: %v %+v", err, f)
	}
	var out string
	var calls int
	h.st.R.QueryRow("select output_json, calls from aul_runs where task_id=?", tk.ID).Scan(&out, &calls)
	if out != `{"count":1}` || calls != 2 {
		t.Fatalf("run record: %s %d", out, calls)
	}
	var models int
	h.st.R.QueryRow("select count(*) from usage_records where task_id=?", tk.ID).Scan(&models)
	if models != 0 {
		t.Fatalf("a workflow used the model %d times", models)
	}
	// A URL from the inputs outside the declared domains: refused by the Core, nothing fetched.
	bad, _ := h.eng.RunWorkflow(ctx, "solar_digest", map[string]any{"pages": []any{"http://169.254.169.254/latest/meta-data"}}, task.Budget{})
	if d := h.wait(bad.ID, task.Succeeded, task.Failed); d.State != task.Failed {
		t.Fatalf("out-of-scope URL: %s", d.State)
	}
	var denied int
	h.st.R.QueryRow("select count(*) from audit_events where type='CAPABILITY_DENIED' and actor='aul' and task_id=?", bad.ID).Scan(&denied)
	if denied != 1 {
		t.Fatalf("refusal not audited: %d", denied)
	}
	// Tampered IR in the database (an extra, undeclared call) is refused before running.
	var irj string
	h.st.R.QueryRow("select ir_json from aul_workflows where id=?", w.ID).Scan(&irj)
	tampered := strings.Replace(irj, `"workspace.write"`, `"workspace.read"`, 1)
	h.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("update aul_workflows set ir_json=? where id=?", tampered, w.ID)
		return err
	})
	tt, _ := h.eng.RunWorkflow(ctx, "solar_digest", map[string]any{"pages": []any{"http://" + h.host + "/one"}}, task.Budget{})
	if d := h.wait(tt.ID, task.Succeeded, task.Failed); d.State != task.Failed || deref(d.ReasonCode) != taxonomy.AULIntegrity {
		t.Fatalf("tampered IR: %s %v", d.State, deref(d.ReasonCode))
	}
	// Versions go up; the new one replaces the old; a disabled workflow does not run.
	if _, _, err := h.eng.RegisterWorkflow(ctx, aulDigest(h.host, 2), "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.eng.RegisterWorkflow(ctx, strings.Replace(aulDigest(h.host, 1), "Pages", "P", 1), "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a lower version was registered: %v", err)
	}
	cur, _ := h.eng.GetWorkflow(ctx, "solar_digest")
	if cur.Version != 2 {
		t.Fatalf("active version %d", cur.Version)
	}
	h.eng.DisableWorkflow(ctx, cur.ID, "owner")
	if _, err := h.eng.RunWorkflow(ctx, "solar_digest", map[string]any{"pages": []any{}}, task.Budget{}); err == nil {
		t.Fatal("a disabled workflow ran")
	}
	if _, err := h.eng.RunWorkflow(ctx, cur.ID, map[string]any{"pages": []any{}, "extra": 1}, task.Budget{}); err == nil {
		t.Fatal("unknown input accepted")
	}
	_ = json.Valid
}
