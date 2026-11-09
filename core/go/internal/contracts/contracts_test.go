package contracts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
