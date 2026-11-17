# ADR-0021 — Second model provider and web search providers

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Second provider:** an OpenAI-compatible chat-completions adapter (`providers.openai_compatible[]`: id, base_url,
  key_file). Routes name their provider; the gateway falls back across providers in tier order. This covers Groq,
  Together, Google's OpenAI-compatible endpoint, or a self-hosted model. Acceptance needs an owner key; until then it is
  tested with a fake server.
- **web.search:** provider interface with Wikipedia (keyless official API) and Brave Search (key file). Searches go
  through the Core's SSRF-guarded HTTP client with a fixed host per provider. Results are recorded as provenance
  (ADR-0017). Other providers can be added behind the same interface.
