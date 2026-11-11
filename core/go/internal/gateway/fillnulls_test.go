package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// A required field that may be null and was left out is read as null; a
// required field that may not be null is still an error.
func TestOmittedNullableFieldIsNull(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["text","scene","view"],"properties":{
	 "text":{"type":"string"},"scene":{"type":["object","null"]},
	 "view":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["type","say"],
	   "properties":{"type":{"type":"string"},"say":{"type":["string","null"]}}}}}}`)
	s, err := compileSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := validate(s, raw, `{"text":"hai","view":[{"type":"text"}]}`)
	if err != nil {
		t.Fatalf("omitted nullable fields rejected: %v", err)
	}
	if !strings.Contains(string(out), `"scene":null`) || !strings.Contains(string(out), `"say":null`) {
		t.Fatalf("not filled: %s", out)
	}
	if _, err := validate(s, raw, `{"scene":null,"view":[]}`); err == nil {
		t.Fatal("a missing non-nullable field must still fail")
	}
	if _, err := validate(s, raw, `{"text":"hai","view":[{"say":"x"}]}`); err == nil {
		t.Fatal("a missing non-nullable nested field must still fail")
	}
	if _, err := validate(s, raw, `{"text":"hai","view":[],"extra":1}`); err == nil {
		t.Fatal("unknown fields must still fail")
	}
}
