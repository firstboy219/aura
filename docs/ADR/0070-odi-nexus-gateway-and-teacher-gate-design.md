# ADR-0070 — ODI: NEXUS gateway, AURA-owned fallback and Teacher Gate (design only)

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Context
Blueprint §7: all runtime cognition goes through NEXUS; an external AI provider is allowed only when objective health evidence shows NEXUS unavailable, or the owner disables NEXUS; AURA alone authorizes it. Today AURA's gateway (`internal/gateway`) walks `model_tiers` priorities across OpenRouter and Gemini on errors and cost. Owner decisions (2026-11-16): the current external routes stay until NEXUS passes its gate; NEXUS may *learn* from external AI only through AURA (Teacher Gate); NEXUS must never refuse an owner request merely because it does not know.

## Design (to implement in A4, after the NEXUS contract tests pass)
- **NEXUS client** in the Core behind the contract in `contracts/odi/nexus/` (service identity, idempotency, deadline, cancel, trace). Health evidence (timestamps, failure codes, hysteresis) is read from NEXUS; AURA decides.
- **Fallback grant** (`FallbackAuthorization`): task, purpose, trigger code, health-evidence reference or owner-disable event, provider/model, permitted data classes and fields, budget cap, expiry, audit id. Denied when AURA's policy cannot be evaluated. Revoked on NEXUS recovery.
- **Teacher Gate**: NEXUS sends a *learning request* (capability gap, sanitized question, expected form). AURA applies the same grant rules (per gap, provider, budget, expiry, audit, owner kill switch), calls the provider, and returns the answer to NEXUS as untrusted data for quarantine and validation. Personal content is not sent unless it can be sanitized, or the owner approves the sanitized version. Provider terms on using outputs for training are checked per provider before activation.
- **Not a trigger**: low confidence, missing knowledge, latency, queue depth, cost, or "a better model". (The Teacher Gate is a *learning* path, not a serving path.)
- **Egress**: provider credentials and calls only through this gateway; default-deny network egress for the Core is an A4/A9 item (the host firewall is off today).
- Until then: the existing tiers and keys (OpenRouter key shared with Jarvis, verified identical on 2026-11-16) stay as they are; no key is rotated or revoked before every consumer is mapped and the owner approves.

## Tests
AT-01, AT-02, AT-03 (see `acceptance-catalogue.json`) plus the NEXUS conformance tests in `ODI_CONTRACTS`.
