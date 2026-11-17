# ADR-0081 — Spatial session authorization (JARVIS blueprint v1.4 §6, §14, §16)

**Proposed** — awaiting owner approval · Date: 2026-11-17 · Not implemented.

## Context
JARVIS blueprint v1.4 adds Spatial Intelligence. The authority chain is: JARVIS requests a scoped spatial session → **AURA authorizes** → AMPI perceives approved input → AURA binds context and may invoke NEXUS → AURA applies policy → JARVIS presents. Today AURA has only the AMPI *intake* (ADR-0080: signed packets stored once, nothing started). No route exists for a spatial session, and AURA holds no AMPI client credential.

## Proposal
1. `POST /api/v1/spatial/sessions` (owner or approver only): body = purpose, modalities, capability profile, device manifest (names only), duration (≤ 30 min, renewable by a new request), capture scope, retention preference, consent reference, foreground/background. AURA returns the authorized scope, expiry, policy result and an AMPI session ref. **Background capture is refused** in the first version; retention default `session_ephemeral`; room mapping, persistent/shared/geographic anchors refused unless a separate owner approval is recorded.
2. `POST /api/v1/spatial/sessions/{id}/stop` and `/revoke`: immediate; revocation closes the AMPI session and invalidates signed media refs.
3. AURA keeps a **session record and audit chain entries** (no frames, no precise location, no scene content). Frames go JARVIS → AMPI only through short-lived signed upload refs minted by AURA for that session; AMPI results return as ADR-0080 packets, which AURA validates (JARVIS `spatial-contract-1.0.json`) and forwards to the JARVIS stream with sequence numbers and replay.
4. AURA, not JARVIS, invokes NEXUS over bounded evidence when reasoning is needed; NEXUS output is never permission.
5. A credential for AMPI (`ampi_client_token`) is stored in AURA's vault (owner action); AURA never prints it.

## Consequences
New migration (spatial_sessions, no content columns), new audit event types, a new policy rule class; tests for denial paths (no consent, expired, revoked, background, oversize scope). AMPI today emits only 2D boxes, session tracks and OCR: a scene graph/depth needs AMPI phase A7 before JARVIS SP-J5.

## Open questions for the owner
S1 pilot scope; retention/legal review for bystanders and room scanning (S5); whether frame upload goes through AURA or directly to AMPI with an AURA-minted ref (this ADR proposes the latter).
