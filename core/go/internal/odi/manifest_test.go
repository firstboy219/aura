package odi

import (
	"testing"

	"aura.local/core/internal/policy"
)

// Drift guard: every compiled tool has declared manifest facts; a new tool cannot be added to policy.Manifest without declaring them.
func TestEveryCompiledToolHasAValidManifest(t *testing.T) {
	if len(policy.Manifest) < 30 {
		t.Fatalf("expected the compiled tool set, got %d", len(policy.Manifest))
	}
	for id := range policy.Manifest {
		m, ok := ManifestOf(id)
		if !ok {
			t.Fatalf("%s: no manifest", id)
		}
		if bad := m.Validate(); len(bad) > 0 {
			t.Errorf("%s: %v", id, bad)
		}
	}
	for id := range declared {
		if _, ok := policy.Lookup(id); !ok {
			t.Errorf("manifest facts declared for %q, which is not a compiled tool (stale entry)", id)
		}
	}
}

func TestManifestFactsFollowTheTrustedEntry(t *testing.T) {
	click, _ := ManifestOf("browser.click")
	if click.SideEffect != "write_external" || !click.NeedsOwner || click.Tier != "A2" || click.Risk != "R3" || click.Idempotent {
		t.Fatalf("%+v", click)
	}
	write, _ := ManifestOf("workspace.write")
	if write.Tier != "args" || !write.NeedsOwner || !write.Idempotent {
		t.Fatalf("%+v", write)
	}
	calc, _ := ManifestOf("calc")
	if calc.Network != "none" || calc.NeedsOwner || calc.Risk != "R0" {
		t.Fatalf("%+v", calc)
	}
	shell, _ := ManifestOf("shell.run")
	if shell.Network != "sandbox_egress_proxy" || shell.Tier != "args" {
		t.Fatalf("%+v", shell)
	}
}

func TestDynamicToolsGetOwnerDeclaredManifests(t *testing.T) {
	policy.RegisterMCPTool("mcp.test_tool", "read", policy.A0)
	policy.RegisterConnectorTool("connector.test_conn")
	policy.RegisterL2Tool("l2.test_calc", 1)
	defer func() { manifestCleanup() }()
	for id, want := range map[string]string{"mcp.test_tool": "declared_by_owner", "connector.test_conn": "declared_by_owner", "l2.test_calc": "none"} {
		m, ok := ManifestOf(id)
		if !ok || m.Network != want || !m.Declared || len(m.Validate()) > 0 {
			t.Errorf("%s: %+v validate=%v", id, m, m.Validate())
		}
	}
}

func manifestCleanup() {
	policy.UnregisterL2Tool("l2.test_calc")
	policy.Manifest["mcp.test_tool"] = policy.ToolSpec{}
	delete(policy.Manifest, "mcp.test_tool")
	delete(policy.Manifest, "connector.test_conn")
}

func TestValidateRefusesIncompleteManifests(t *testing.T) {
	for name, m := range map[string]ToolManifest{
		"no side effect":  {ToolID: "x", Version: "1", Network: "none", DataClasses: []string{"public"}, Declared: true},
		"undeclared":      {ToolID: "x", Version: "1", SideEffect: "read", Network: "none", DataClasses: []string{"public"}},
		"bad network":     {ToolID: "x", Version: "1", SideEffect: "read", Network: "anywhere", DataClasses: []string{"public"}, Declared: true},
		"no data classes": {ToolID: "x", Version: "1", SideEffect: "read", Network: "none", Declared: true},
		"bad data class":  {ToolID: "x", Version: "1", SideEffect: "read", Network: "none", DataClasses: []string{"top-secret"}, Declared: true},
		"external write, no network, no approval": {ToolID: "x", Version: "1", SideEffect: "write_external", Network: "none", DataClasses: []string{"public"}, Declared: true},
	} {
		if len(m.Validate()) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}
