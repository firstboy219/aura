# ADR-0073 — ODI A4: NEXUS gateway, AURA-owned fallback, Teacher Gate

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
Implements the design in ADR-0070.

## Decision
1. **Mode, default `off`.** `internal/nexus.Router` is installed in the gateway (`Gateway.SetCognition`). Mode `off`: the legacy providers serve exactly as before (the router only reads one settings row). Mode `shadow`: legacy serves, NEXUS is called beside it and only sizes and timing are recorded in `nexus_shadow` (no content). Mode `primary`: NEXUS serves; an external provider is reachable only through a FallbackAuthorization. The owner sets the mode (`PUT /api/v1/admin/nexus`); **switching to primary is refused unless NEXUS answers consecutive readiness probes now** (the recovery evidence rule).
2. **Contract.** `internal/nexus.Client` speaks `contracts/odi/nexus` (service identity token from the vault secret `connector.nexus_service_token`). A response is rejected unless `execution_authority` is `none` and the request id matches. A gateway request travels as `input.text` = `AURA-LLM-REQUEST/1\n<json>` (adapter v0); structured output is validated with the gateway's own schema validator, so a malformed NEXUS answer is an error, not a reason to call another model.
3. **Health evidence with hysteresis** (`Monitor`). Only `MODEL_UNAVAILABLE`, `KNOWLEDGE_UNAVAILABLE`, `INTERNAL_ERROR` and transport failures count. Rate limiting, overload, deadline, insufficient evidence, bad requests and low confidence **never** do. Proposed thresholds (owner approval required, §10.5): 3 consecutive failures within 60 s → `UNAVAILABLE`; 2 consecutive successes and a 30 s hold-down → `HEALTHY`; evidence older than 45 s → `UNKNOWN`. One timeout is `DEGRADED`, not unavailable.
4. **FallbackAuthorization** (`Authorizer`, table `fallback_authorizations`). Valid triggers: `NEXUS_UNAVAILABLE` (needs `UNAVAILABLE` plus its evidence reference) and `OWNER_DISABLED` (the owner's explicit switch-off, `POST /admin/nexus/owner-disable`, time-boxed). Scoped by task, purpose, data classes, providers, budget cap, call count and expiry; every decision (granted or denied) is audited. Denied when: the health state is unknown, degraded or healthy; the policy cannot be read; the owner named no provider (**the default**); the data class is not permitted (default: public only). An authorization that is used up is not renewed by asking again. The legacy path then runs restricted to the granted providers and budget (`Request.OnlyProviders`).
5. **Recovery.** Leaving `UNAVAILABLE` revokes every active authorization (`NEXUS_RECOVERED`); the owner re-enabling NEXUS does the same. New calls go to NEXUS.
6. **Teacher Gate** (`POST /api/v1/teacher/requests`, table `teacher_requests`). Its own service token (`connector.teacher_service_token`), checked in the handler; that token opens nothing else and the owner's token does not open it. Disabled until the owner enables it **and** names a provider. A question must pass the sanitizer (no secrets, e-mails, phone numbers, long numbers; ≤1500 chars), is sent as `public`, within a per-request budget and a daily limit. The answer is returned as `trust: untrusted, source_type: external-ai` for NEXUS to quarantine and validate; it is a learning path and is never shown to the owner by AURA.

## What this does not do
No real NEXUS exists, so no end-to-end run against NEXUS has happened; shadow measurements need one. AURA's own network egress is not yet allow-listed (A9). The existing OpenRouter/Gemini tiers and keys are untouched; the shared OpenRouter key is not rotated.

## Rollback
Set mode `off` (one PUT) or revert the commit; migration 0020 only adds tables.

## Owner decisions
The proposed health thresholds; which providers (if any) may serve as fallback and for the Teacher Gate; fallback budget, call limit and data classes; whether to keep the `AURA-LLM-REQUEST/1` adapter or change the NEXUS contract.
