// Package odi holds the ODI contract tests (Blueprint v1.1 §18, §23, A1): the canonical task-state mapping, the error
// catalogue, the event envelope, the NEXUS gateway contract and the acceptance catalogue. They read contracts/odi and
// change no behaviour. Acceptance tests that cannot pass yet live behind the odi_acceptance build tag (acceptance_test.go).
package odi

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"aura.local/core/internal/store"
	"aura.local/core/internal/task"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const dir = "../../../../contracts/odi"

func readJSON(t *testing.T, rel string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

func schema(t *testing.T, rel string) *jsonschema.Schema {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	c := jsonschema.NewCompiler()
	url := "https://aura.local/contracts/odi/" + filepath.Base(rel)
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(url)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return s
}

func valid(t *testing.T, s *jsonschema.Schema, doc string) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(inst)
}

type mapping struct {
	Canonical []string `json:"canonical"`
	Current   map[string]struct {
		Canonical *string `json:"canonical"`
		Status    string  `json:"status"`
	} `json:"current"`
	NotImplemented map[string]string `json:"not_implemented"`
}

// The states the tasks table really allows (read from the migrated schema, not from a copy in this test).
func schemaStates(t *testing.T) []string {
	t.Helper()
	d := t.TempDir()
	s, err := store.Open(context.Background(), filepath.Join(d, "aura.db"), filepath.Join(d, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var ddl string
	err = s.Conn(context.Background(), func(ctx context.Context, c *sql.Conn) error {
		return c.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name='tasks'`).Scan(&ddl)
	})
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`state\s+TEXT\s+NOT\s+NULL\s+CHECK\s*\(\s*state\s+IN\s*\(([^)]*)\)`).FindStringSubmatch(ddl)
	if m == nil {
		t.Fatalf("no state CHECK in tasks DDL: %s", ddl)
	}
	var out []string
	for _, p := range strings.Split(m[1], ",") {
		out = append(out, strings.Trim(strings.TrimSpace(p), "'"))
	}
	sort.Strings(out)
	return out
}

func TestStateMappingCoversEveryStateOfTheSchemaAndTheCode(t *testing.T) {
	var m mapping
	readJSON(t, "state-mapping.json", &m)
	fromSchema := schemaStates(t)
	var fromCode []string
	for _, s := range []task.State{task.Created, task.Planning, task.Running, task.WaitingApproval, task.WaitingInput, task.Waiting, task.Scheduled,
		task.Paused, task.Succeeded, task.Failed, task.Cancelled, task.BudgetExceeded} {
		fromCode = append(fromCode, string(s))
	}
	sort.Strings(fromCode)
	var mapped []string
	for k := range m.Current {
		mapped = append(mapped, k)
	}
	sort.Strings(mapped)
	if strings.Join(fromSchema, ",") != strings.Join(mapped, ",") {
		t.Fatalf("tasks.state CHECK %v differs from state-mapping.json %v: map every state", fromSchema, mapped)
	}
	if strings.Join(fromCode, ",") != strings.Join(mapped, ",") {
		t.Fatalf("task.State constants %v differ from state-mapping.json %v", fromCode, mapped)
	}
	canon := map[string]bool{}
	for _, c := range m.Canonical {
		canon[c] = true
	}
	if len(canon) != 18 {
		t.Fatalf("Blueprint §5 lists 18 canonical states, have %d", len(canon))
	}
	covered := map[string]bool{}
	for cur, e := range m.Current {
		switch e.Status {
		case "mapped", "proposed":
			if e.Canonical == nil || !canon[*e.Canonical] {
				t.Errorf("%s: %s must name a canonical state", cur, e.Status)
				continue
			}
			covered[*e.Canonical] = true
		case "extension":
			if e.Canonical != nil {
				t.Errorf("%s: an extension has no canonical state", cur)
			}
		default:
			t.Errorf("%s: unknown status %q", cur, e.Status)
		}
	}
	for c := range canon {
		_, todo := m.NotImplemented[c]
		if !covered[c] && !todo {
			t.Errorf("canonical %s is neither mapped nor listed in not_implemented", c)
		}
		if covered[c] && todo {
			t.Errorf("canonical %s is both mapped and listed in not_implemented", c)
		}
	}
}

func TestErrorCatalogueHasEveryBlueprintCode(t *testing.T) {
	var cat struct {
		Errors []struct {
			Code      string `json:"code"`
			Retryable *bool  `json:"retryable"`
			HTTP      int    `json:"http"`
		} `json:"errors"`
	}
	readJSON(t, "error-catalogue.json", &cat)
	want := strings.Fields(`AUTHN_REQUIRED AUTHN_INVALID AUTHZ_DENIED POLICY_HARD_DENY APPROVAL_REQUIRED APPROVAL_EXPIRED APPROVAL_REPLAYED APPROVAL_DIGEST_MISMATCH
		GRANT_EXPIRED GRANT_REVOKED GRANT_SCOPE_MISMATCH NEXUS_UNAVAILABLE NEXUS_HEALTH_UNKNOWN FALLBACK_NOT_AUTHORIZED WORKER_UNAVAILABLE WORKER_PROTOCOL_INVALID
		WORKER_ISOLATION_VIOLATION IDEMPOTENCY_CONFLICT TASK_STATE_CONFLICT DEADLINE_EXCEEDED BUDGET_EXCEEDED VERIFICATION_FAILED OUTCOME_UNCERTAIN RECONCILIATION_REQUIRED
		EGRESS_DENIED DESTINATION_NOT_ALLOWED DATA_CLASS_NOT_ALLOWED`)
	have := map[string]bool{}
	for _, e := range cat.Errors {
		if have[e.Code] {
			t.Errorf("duplicate %s", e.Code)
		}
		have[e.Code] = true
		if e.Retryable == nil || e.HTTP < 200 || e.HTTP > 599 {
			t.Errorf("%s: needs retryable and an HTTP status", e.Code)
		}
	}
	for _, c := range want {
		if !have[c] {
			t.Errorf("missing %s (Blueprint Appendix B)", c)
		}
	}
	if len(cat.Errors) != len(want) {
		t.Errorf("catalogue has %d codes, Appendix B has %d", len(cat.Errors), len(want))
	}
}

func TestEventEnvelope(t *testing.T) {
	s := schema(t, "event-envelope.schema.json")
	ok := `{"event_id":"e1","event_type":"task.state","schema_version":"1.0","sequence":3,"occurred_at":"2026-11-16T14:00:00Z","recorded_at":"2026-11-16T14:00:01Z","actor_ref":"core","task_id":"t1","correlation_id":"c1","classification":"internal"}`
	if err := valid(t, s, ok); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	for name, doc := range map[string]string{
		"no sequence":   strings.Replace(ok, `"sequence":3,`, ``, 1),
		"sequence 0":    strings.Replace(ok, `"sequence":3`, `"sequence":0`, 1),
		"bad class":     strings.Replace(ok, `"internal"`, `"secret"`, 1),
		"unknown field": strings.Replace(ok, `"task_id":"t1"`, `"task_id":"t1","extra":1`, 1),
	} {
		if valid(t, s, doc) == nil {
			t.Errorf("%s: invalid envelope accepted", name)
		}
	}
}

func TestNexusGatewayContractExamples(t *testing.T) {
	req, res, errS := schema(t, "nexus/cognitive-request.schema.json"), schema(t, "nexus/cognitive-result.schema.json"), schema(t, "nexus/error-envelope.schema.json")
	okReq := `{"request_id":"7d9d3a5e-0000-4000-8000-000000000001","task_id":"t1","session_id":"s1","caller":{"platform":"aura","service_id":"aura-core"},"purpose":"analysis",
		"scope":{"workspace_id":"w1","allowed_knowledge":["kb:approved"]},"classification":"internal","deadline":"2026-11-16T15:00:00Z","idempotency_key":"k1",
		"input":{"text":"hello","evidence_refs":[]},"context":{"context_id":"c1","token_budget":8192},"schema_version":"1.0"}`
	if err := valid(t, req, okReq); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if valid(t, req, strings.Replace(okReq, `"platform":"aura"`, `"platform":"jarvis"`, 1)) == nil {
		t.Error("only AURA may call NEXUS (Blueprint §2.2)")
	}
	if valid(t, req, strings.Replace(okReq, `"idempotency_key":"k1",`, ``, 1)) == nil {
		t.Error("idempotency_key is mandatory")
	}
	okRes := `{"request_id":"7d9d3a5e-0000-4000-8000-000000000001","status":"SUCCEEDED","result":{"content":"x"},"confidence":{"band":"medium","method":"evaluated-calibration-v1"},
		"execution_authority":"none","trace_id":"tr1","created_at":"2026-11-16T14:00:00Z","schema_version":"1.0"}`
	if err := valid(t, res, okRes); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	if valid(t, res, strings.Replace(okRes, `"none"`, `"full"`, 1)) == nil {
		t.Error("NEXUS output is advisory: execution_authority must be none")
	}
	okErr := `{"error":{"code":"MODEL_UNAVAILABLE","message":"x","retryable":false,"request_id":"r","trace_id":"t","schema_version":"1.0"}}`
	if err := valid(t, errS, okErr); err != nil {
		t.Fatalf("valid error rejected: %v", err)
	}
	if valid(t, errS, strings.Replace(okErr, `MODEL_UNAVAILABLE`, `SOMETHING_ELSE`, 1)) == nil {
		t.Error("unknown NEXUS error code accepted")
	}
}

func TestAcceptanceCatalogueIsComplete(t *testing.T) {
	var cat struct {
		Tests []struct{ ID, Title, Status, Phase, Evidence string } `json:"tests"`
	}
	readJSON(t, "acceptance-catalogue.json", &cat)
	if len(cat.Tests) != 12 {
		t.Fatalf("Blueprint Appendix C has 12 acceptance tests, have %d", len(cat.Tests))
	}
	okStatus := map[string]bool{"met": true, "partial": true, "unproven": true, "not_met": true, "not_applicable_yet": true}
	phase := regexp.MustCompile(`^A[1-9]$`)
	for i, a := range cat.Tests {
		if a.ID != "AT-"+twoDigits(i+1) || !okStatus[a.Status] || !phase.MatchString(a.Phase) || strings.TrimSpace(a.Evidence) == "" || strings.TrimSpace(a.Title) == "" {
			t.Errorf("entry %d (%s) is incomplete or has an invalid status/phase: %+v", i+1, a.ID, a)
		}
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
