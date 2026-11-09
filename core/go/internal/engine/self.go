package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"aura.local/core/internal/policy"
)

// SelfDescription is AURA's answer to "who are you, what can you do, what are
// your weaknesses, how do you improve?". Every fact comes from the Core's own
// records: the model registry, the tool manifest, measured results and the
// Limitation Ledger. No model writes it, so it cannot flatter itself.
func (e *Engine) SelfDescription(ctx context.Context) (map[string]any, error) {
	sm, err := e.SelfModel(ctx)
	if err != nil {
		return nil, err
	}
	tiers := map[string][]map[string]any{}
	for tier, routes := range e.cfg.Models {
		for _, r := range routes {
			tiers[tier] = append(tiers[tier], map[string]any{"display_name": r.DisplayName, "route_id": r.RouteID, "free": r.Free,
				"may_log_prompts": r.MayLog(), "provider": firstNonEmpty(r.Provider, "openrouter")})
		}
	}
	var tools []string
	for id := range policy.Manifest {
		tools = append(tools, id)
	}
	sort.Strings(tools)
	ls, _ := e.ListLimitations(ctx, "")
	counts := map[string]int{}
	for _, l := range ls {
		counts[l.Status]++
	}
	spent := e.improvementSpentToday(ctx)
	skills, _ := e.ListSkills(ctx)
	activeSkills := 0
	for _, s := range skills {
		if s.Status == "active" {
			activeSkills++
		}
	}
	mem, _ := e.ListMemories(ctx, "active", 1000)
	open := e.OpenLimitationsDisclosure(ctx)
	var sb strings.Builder
	fmt.Fprintf(&sb, "I am AURA, an autonomous agent runtime that works for its owner. I am an engineering system: I do not have consciousness, feelings or faith, and I will not claim to.\n")
	fmt.Fprintf(&sb, "My reasoning is done by language models chosen by tier: ")
	var parts []string
	for _, t := range []string{"fast", "standard", "deep"} {
		if rs := tiers[t]; len(rs) > 0 {
			parts = append(parts, fmt.Sprintf("%s → %s", t, rs[0]["display_name"]))
		}
	}
	sb.WriteString(strings.Join(parts, "; ") + ". Free routes (which may log prompts) only ever see tasks you mark public.\n")
	if t, ok := sm["tasks"].(map[string]any); ok {
		fmt.Fprintf(&sb, "Measured so far: %v tasks finished, %v completed, %v verified, %v needed a replan.\n", t["finished"], t["succeeded"], t["verified"], t["replanned"])
	}
	if len(open) == 0 {
		sb.WriteString("I have no open limitations recorded right now (that means none were measured, not that none exist).\n")
	} else {
		sb.WriteString("My open limitations:\n")
		for _, l := range open {
			sb.WriteString("- " + l + "\n")
		}
	}
	fmt.Fprintf(&sb, "How I improve: I record a limitation only from measured evidence, diagnose it from a fixed list of causes, change only what my class of remedy allows, re-test on the tasks that exposed it, and roll back if it does not help. I never widen my own permissions. Improvement work spent $%.4f of its $%.2f daily cap today.", spent, e.ledgerCfg.ImprovementUSDDaily)
	return map[string]any{
		"identity":        Identity,
		"version":         e.version,
		"summary":         sb.String(),
		"model_tiers":     tiers,
		"tools":           tools,
		"charter":         e.CharterStatus(),
		"skills_active":   activeSkills,
		"memories_active": len(mem),
		"self_model":      sm,
		"ledger":          map[string]any{"counts": counts, "improvement_spent_today_usd": spent, "improvement_cap_usd": e.ledgerCfg.ImprovementUSDDaily},
		"evidence_level":  "implementation-verified for the measured parts; nothing here is production-proven",
		"not_claimed":     []string{"consciousness", "feelings", "religious authority", "security beyond the tested controls"},
	}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
