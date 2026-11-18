# ADR-0084: JARVIS's monthly AI budget is enforced at AURA's model gateway

- Status: accepted (owner 2026-11-18: "Ok kerjakan semua" on the finding that AURA only recorded JARVIS's spend)
- Date: 2026-11-18

## Context

ADR-0083 made AURA JARVIS's only way to outside AI, but the monthly budget was still checked only inside JARVIS. AURA holds the keys, so
it must also be able to stop the spending.

## Decision

- Settings row `odi.llmgate.jarvis_budget` = `{"monthly_usd": N}`; unset = $20 (JARVIS's budget on 2026-11-18); 0 = no cap.
- `GET /api/v1/admin/llmgate` (budget, this month's spend and calls, month start in the workspace time zone);
  `PUT /api/v1/admin/llmgate` (owner role only, audited `LLMGATE_BUDGET_SET`). JARVIS sends the owner's budget here whenever he changes it.
- Before a paid call (OpenRouter, a model that is not `:free` / `openrouter/free`, on chat, audio or embeddings) the gate sums this month's
  `usage_records` of tier `jarvis`; at or over the cap it answers 402 `budget_exhausted` without calling out, and records outcome `budget`.
  JARVIS treats a 402 like an empty balance: free models serve.
- Free tiers (Groq, Gemini, OpenRouter free models) are never stopped by the cap.

## Consequences

- Two checks with the same number: JARVIS's (per category, warnings) and AURA's (the hard stop that holds even if JARVIS is wrong).
- Cost is known only where the provider reports it (OpenRouter's usage chunk); Deepgram is not counted.
