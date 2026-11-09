package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aura.local/core/internal/rpc"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaDir = "../../../../contracts/schemas"

func load(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(schemaDir, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func compile(t *testing.T, name, pointer string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	abs, _ := filepath.Abs(filepath.Join(schemaDir, name+".json"))
	s, err := c.Compile("file://" + abs + pointer)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, v any) error {
	b, _ := json.Marshal(v)
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

func TestEveryMethodHasASchema(t *testing.T) {
	for _, m := range append([]string{"envelope", "response"}, Methods...) {
		s := load(t, m)
		if s["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("%s: wrong $schema %v", m, s["$schema"])
		}
	}
}

func TestEnvelopeListsExactlyTheMethods(t *testing.T) {
	env := load(t, "envelope")
	enum := env["properties"].(map[string]any)["method"].(map[string]any)["enum"].([]any)
	got := map[string]bool{}
	for _, e := range enum {
		got[e.(string)] = true
	}
	if len(got) != len(Methods) {
		t.Fatalf("envelope has %d methods, want %d", len(got), len(Methods))
	}
	for _, m := range Methods {
		if !got[m] {
			t.Errorf("envelope missing %s", m)
		}
	}
}

// Messages produced by the Go codec validate against the shared schemas.
func TestGoEnvelopesMatchSchemas(t *testing.T) {
	envS := compile(t, "envelope", "")
	respS := compile(t, "response", "")
	req := rpc.Envelope{JSONRPC: "2.0", ID: "c-01J0000000000000000000000", Method: "task.plan", Params: json.RawMessage(`{}`),
		Meta: &rpc.Meta{TaskID: "01J00000000000000000000000", TraceID: "t", CorrelationID: "c", DeadlineUnixMs: 1,
			CapabilityToken: strings.Repeat("a", 43)}}
	if err := validate(t, envS, req); err != nil {
		t.Fatalf("request: %v", err)
	}
	ok := rpc.Envelope{JSONRPC: "2.0", ID: "w-1", Result: json.RawMessage(`{"status":"ok"}`)}
	if err := validate(t, respS, ok); err != nil {
		t.Fatalf("response: %v", err)
	}
	bad := rpc.Envelope{JSONRPC: "2.0", ID: "w-1", Error: rpc.Errf(rpc.CodeDenied, "capability_denied", "denied")}
	if err := validate(t, respS, bad); err != nil {
		t.Fatalf("error response: %v", err)
	}
	unknown := map[string]any{"jsonrpc": "2.0", "id": "x", "method": "shell.exec", "params": map[string]any{},
		"meta": map[string]any{"trace_id": "t", "correlation_id": "c", "deadline_unix_ms": 0}}
	if validate(t, envS, unknown) == nil {
		t.Fatal("schema accepted an unknown method")
	}
}

func TestPlanAndStepResultSchemas(t *testing.T) {
	plan := compile(t, "task.plan", "#/properties/result")
	good := map[string]any{"steps": []any{map[string]any{"step_no": 1, "description": "d", "tool_id": "web.fetch", "url": "https://a", "path": nil}},
		"completion_criteria": []any{"x"}}
	if err := validate(t, plan, good); err != nil {
		t.Fatal(err)
	}
	evil := map[string]any{"steps": []any{map[string]any{"step_no": 1, "description": "d", "tool_id": "shell", "url": nil, "path": nil}},
		"completion_criteria": []any{"x"}}
	if validate(t, plan, evil) == nil {
		t.Fatal("plan schema accepted an unknown tool")
	}
	step := compile(t, "task.step", "#/properties/result")
	if err := validate(t, step, map[string]any{"decision": "step_done", "reason": "ok"}); err != nil {
		t.Fatal(err)
	}
}
