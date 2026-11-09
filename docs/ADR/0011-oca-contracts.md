# ADR-0011 — Owner Communication Agent contracts (contract only; implementation P5)

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision (contract)
- Transport: WebSocket audio session terminated by Go Core (session gateway); providers behind a neutral interface
  `SpeechInput` (streaming partial/final transcripts with confidence, language tags, turn events) and `SpeechOutput`
  (streaming chunks, cancel on barge-in).
- Turn management is owned by AURA (VAD/endpointing + provider turn events), independent of any single provider.
- Partial transcripts never trigger actions. Voice can *initiate* an approval; consequential A2 approval requires
  confirmation in the authenticated client bound to the action hash (§8.3, D.1 P5). Voice biometrics never sole control.
- Privacy: no raw audio retained by default; provider-transfer consent recorded; mute/stop always available.
No code in P0/P1. Jarvis's existing voice stack is not touched or reused.
