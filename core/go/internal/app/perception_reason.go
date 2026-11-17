package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aura.local/core/internal/ids"
	"aura.local/core/internal/nexus"
)

// reasonOverEvidence asks NEXUS — and only NEXUS — to answer the owner's question from AMPI's evidence. The evidence is personal (the owner's voice, room, documents): it is classified
// confidential, so NEXUS's Teacher Gate never forwards it, and there is no external fallback on this path. The system prompt is identical across requests so NEXUS reuses its cached prefix.
// warmNexus has NEXUS read the perception system prompt (one output token) while AMPI is still working, so its prefix is in the prompt cache when the evidence comes.
func (a *App) warmNexus(ctx context.Context, system string) {
	if a.Nexus == nil {
		return
	}
	a.askNexus(ctx, system, "(siap)", 1)
}

func (a *App) reasonOverEvidence(ctx context.Context, system, user string) (string, string, error) {
	return a.askNexus(ctx, system, user, 120) // decode is the CPU bottleneck (~4 tokens/s): short answers are fast answers
}

func (a *App) askNexus(ctx context.Context, system, user string, maxTokens int) (string, string, error) {
	if a.Nexus == nil {
		return "", "", fmt.Errorf("NEXUS is not configured")
	}
	env, _ := json.Marshal(map[string]any{"tier": "fast", "purpose": "analysis", "max_output_tokens": maxTokens,
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}}})
	req := nexus.Request{RequestID: ids.New(), TaskID: "none", SessionID: "none", Caller: nexus.Caller{Platform: "aura", ServiceID: a.Nexus.Router.ServiceID}, Purpose: "analysis",
		Scope: nexus.Scope{WorkspaceID: a.Nexus.Router.WorkspaceID, AllowedKnowledge: []string{"kb:approved"}}, Classification: "confidential",
		Deadline: time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339), IdempotencyKey: ids.New(), Input: nexus.Input{Text: nexus.LLMEnvelope + string(env), EvidenceRefs: []string{}},
		Context: nexus.Ctx{ContextID: ids.New(), TokenBudget: 4096}, SchemaVersion: "1.0"}
	res, err := a.Nexus.Client.Submit(ctx, req)
	if err != nil {
		return "", "", err
	}
	if res.Status != "SUCCEEDED" {
		return "", "", fmt.Errorf("NEXUS status %s: %v", res.Status, res.Result.Limitations)
	}
	return res.Result.Content, res.Model.ModelID, nil
}
