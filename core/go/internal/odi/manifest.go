package odi

import (
	"sort"
	"strings"

	"aura.local/core/internal/policy"
)

// ToolManifest is the Blueprint §10 manifest of one tool, assembled from what the Core really knows: the compiled trusted entry (id, version, side effect,
// approval tier) plus facts declared here for the compiled tools (network destination class, data classes). Nothing in it is a promise the code does not keep:
// timeouts and retries are not per-tool facts in AURA, so they are not listed.
type ToolManifest struct {
	ToolID      string   `json:"tool_id"`
	Version     string   `json:"version"`
	Owner       string   `json:"owner"`       // aura-core (compiled) or owner-declared (mcp./l2./connector.)
	SideEffect  string   `json:"side_effect"` // pure | read | write_idempotent | write_external
	Tier        string   `json:"tier"`        // A0..A3 for default arguments; "args" when arguments decide (workspace.write, shell.run)
	Risk        string   `json:"risk"`        // label under the proposed risk policy; never relaxes enforcement
	Network     string   `json:"network"`     // none | task_allowlist | fixed_public_sources | sandbox_egress_proxy | declared_by_owner
	DataClasses []string `json:"data_classes"`
	Idempotent  bool     `json:"idempotent"` // safe to repeat with the same step idempotency key
	Deprecation string   `json:"deprecation"`
	NeedsOwner  bool     `json:"needs_owner_approval"`
	Declared    bool     `json:"declared"` // false: the tool is registered but no manifest facts were declared for it (a gap)
}

// declared holds the facts the code cannot derive for the compiled tools. A test fails when a compiled tool has no entry here.
var declared = map[string]struct {
	network string
	classes []string
}{
	"web.fetch": {"task_allowlist", []string{"public"}}, "web.search": {"fixed_public_sources", []string{"public"}}, "news.headlines": {"fixed_public_sources", []string{"public"}},
	"info.weather": {"fixed_public_sources", []string{"public"}}, "info.prayer": {"fixed_public_sources", []string{"public"}}, "info.quran": {"fixed_public_sources", []string{"public"}},
	"info.place": {"fixed_public_sources", []string{"public"}}, "info.route": {"fixed_public_sources", []string{"public"}}, "info.prices": {"fixed_public_sources", []string{"public"}},
	"data.read": {"none", []string{"internal", "confidential"}}, "workspace.read": {"none", []string{"internal", "confidential"}}, "workspace.write": {"none", []string{"internal", "confidential"}},
	"calc": {"none", []string{"public", "internal", "confidential"}}, "wait.event": {"none", []string{"internal"}}, "wait.until": {"none", []string{"internal"}}, "agent.delegate": {"none", []string{"internal", "confidential"}},
	"shell.run":    {"sandbox_egress_proxy", []string{"internal"}},
	"browser.open": {"task_allowlist", []string{"public", "internal"}}, "browser.inspect": {"task_allowlist", []string{"public", "internal"}}, "browser.screenshot": {"task_allowlist", []string{"public", "internal"}},
	"browser.scroll": {"task_allowlist", []string{"public", "internal"}}, "browser.wait": {"task_allowlist", []string{"public", "internal"}}, "browser.back": {"task_allowlist", []string{"public", "internal"}},
	"browser.type": {"task_allowlist", []string{"internal", "confidential"}}, "browser.select": {"task_allowlist", []string{"internal", "confidential"}}, "browser.click": {"task_allowlist", []string{"internal", "confidential"}},
	"browser.download": {"task_allowlist", []string{"public", "internal"}}, "browser.upload": {"task_allowlist", []string{"confidential"}}, "browser.login": {"task_allowlist", []string{"restricted"}},
	"browser.mfa": {"task_allowlist", []string{"restricted"}},
}

// ManifestOf assembles the manifest of one registered tool.
func ManifestOf(id string) (ToolManifest, bool) {
	spec, ok := policy.Lookup(id)
	if !ok {
		return ToolManifest{}, false
	}
	m := ToolManifest{ToolID: id, Version: spec.Version, Owner: "aura-core", SideEffect: spec.SideEffect, Deprecation: "none", Idempotent: spec.SideEffect != "write_external"}
	tier := spec.Tier(map[string]any{})
	m.Tier = string(tier)
	switch id {
	case "workspace.write", "shell.run":
		m.Tier = "args"
		m.NeedsOwner = true
	default:
		m.NeedsOwner = tier == policy.A2
	}
	m.Risk = string(policy.RiskOf(spec.SideEffect, tier))
	if d, ok := declared[id]; ok {
		m.Network, m.DataClasses, m.Declared = d.network, d.classes, true
		return m, true
	}
	switch {
	case strings.HasPrefix(id, "mcp."):
		m.Owner, m.Network, m.DataClasses, m.Declared = "owner-declared", "declared_by_owner", []string{"internal"}, true
	case strings.HasPrefix(id, "l2."):
		m.Owner, m.Network, m.DataClasses, m.Declared = "owner-approved", "none", []string{"public", "internal"}, true // WASM sandbox, no network
	case strings.HasPrefix(id, "connector."):
		m.Owner, m.Network, m.DataClasses, m.Declared = "owner-declared", "declared_by_owner", []string{"confidential"}, true
	}
	return m, true
}

// Manifests lists the manifest of every registered tool, sorted by id.
func Manifests() []ToolManifest {
	ids := policy.ToolIDs()
	sort.Strings(ids)
	out := make([]ToolManifest, 0, len(ids))
	for _, id := range ids {
		if m, ok := ManifestOf(id); ok {
			out = append(out, m)
		}
	}
	return out
}

// Validate returns what is wrong with a manifest (empty: fine). Used by registration and by the drift test.
func (m ToolManifest) Validate() []string {
	var bad []string
	if m.ToolID == "" || m.Version == "" {
		bad = append(bad, "tool_id and version are required")
	}
	switch m.SideEffect {
	case "pure", "read", "write_idempotent", "write_external":
	default:
		bad = append(bad, "side_effect must be pure, read, write_idempotent or write_external")
	}
	if !m.Declared {
		bad = append(bad, "no network destination class and data classes declared")
	}
	switch m.Network {
	case "none", "task_allowlist", "fixed_public_sources", "sandbox_egress_proxy", "declared_by_owner":
	default:
		bad = append(bad, "network must be none, task_allowlist, fixed_public_sources, sandbox_egress_proxy or declared_by_owner")
	}
	if len(m.DataClasses) == 0 {
		bad = append(bad, "data_classes is required")
	}
	for _, c := range m.DataClasses {
		switch c {
		case "public", "internal", "confidential", "restricted":
		default:
			bad = append(bad, "unknown data class "+c)
		}
	}
	if m.SideEffect == "write_external" && !m.NeedsOwner && m.Network == "none" {
		bad = append(bad, "a write_external tool with no network and no approval is not plausible")
	}
	return bad
}
