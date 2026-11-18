# ADR-0083: AURA is JARVIS's only way out to an outside AI (model gateway)

- Status: accepted (owner 2026-11-18: "Ya silahkan, sesuai rencana di blueprint ODI aja", after the activity view showed JARVIS, not AURA,
  calling outside models: 635 calls in 24 h straight from JARVIS with its own keys)
- Date: 2026-11-18

## Context

The ODI blueprint makes AURA the sole agent runtime and the holder of provider credentials and the external-AI gate; NEXUS first, outside
models only behind AURA's policy. JARVIS predates ODI and kept its own OpenRouter, Groq and (optionally) Deepgram keys, calling providers
directly for chat, understanding, speech-to-text, voice, embeddings and the model catalog.

## Decision

- `internal/llmgate`: `/api/v1/llm/{provider}/{path}` under AURA's normal authentication. A fixed allowlist of providers (openrouter, groq,
  gemini, deepgram) and of paths per provider (chat/completions, embeddings, audio/transcriptions, audio/speech, credits, key, models,
  generation; Deepgram v1/listen and v1/auth/grant). Anything else is refused (403).
- The key comes from AURA's vault: `connector.jarvis_openrouter` (+ `_chat` / `_latar` by the `X-Key-Class` header, keeping JARVIS's
  separate budgets), `connector.jarvis_groq`, `gemini` (AURA's own Google AI Studio key), `connector.jarvis_deepgram`. JARVIS keeps no
  provider key once it has moved.
- The request JARVIS built is forwarded as is; the answer streams back with a flush per chunk (spoken replies must not wait); a long stream
  gets its own write deadline (15 min) instead of the server's 60 s.
- Every call: one `usage_records` row (tier `jarvis`, provider, model, path, outcome, latency, the provider's reported cost, purpose
  `jarvis:<caller>`) and the egress log (`jarvis-llm`). Content is never stored or logged.
- NEXUS first stays where it can answer (JARVIS's light work via `/api/v1/cognition/complete`, ADR-0082 era); the gateway serves what
  NEXUS cannot (tool-using chat, speech, voice).

## Alternatives

- Leave JARVIS calling providers: rejected by the blueprint and the owner.
- Re-implement every JARVIS model use inside AURA: much larger, and JARVIS's routing (category chains, free mode, health) would be lost.

## Consequences

- JARVIS depends on AURA for outside AI; when AURA is down, JARVIS falls back to NEXUS for calls without tools and says so for the rest.
- JARVIS's spend now appears in AURA's usage records (and so in its budget totals).
- Budget enforcement for JARVIS's calls stays in JARVIS for now (its per-category budgets); AURA records. A later ADR may move enforcement.
