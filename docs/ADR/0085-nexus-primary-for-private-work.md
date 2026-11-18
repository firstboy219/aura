# ADR-0085: NEXUS is the brain for AURA's private work

- Status: accepted (owner 2026-11-18: "Poin 1 setuju, yang performanya NEXUS jadi bagus")
- Date: 2026-11-18

## Context

The NEXUS policy stayed in shadow mode: external models served every AURA task and NEXUS was only measured — and refused 72 of 77
shadow calls because AURA's prompts (p50 ≈ 4.4k tokens, p90 ≈ 6k) did not fit its 4096-token context. Private tasks may use only paid
routes, so with the OpenRouter balance at zero they failed (17 "Cari cara" tasks in three days).

Measured on this host (2 physical cores, Qwen3-4B Q4_K_M, llama.cpp b11749, 16k context, 2026-11-18):
- a cold 6.1k-token prompt: ~9 min (prefill 13.3 t/s with flash attention, 11.8 without);
- the next step of the same task (prompt cache): 64 new tokens, ~25–29 s;
- another task with the same system prompt: 6.1k tokens reused from the cache;
- decode deep in the context: ~0.85 t/s (≈3.7 t/s on a short prompt).

## Decision

- NEXUS: context 16384, flash attention on, the inference timeout raised to 1200 s (the request's own deadline still rules).
- Policy `mode: primary` with `primary_data_classes: ["confidential"]`: private work is served by NEXUS; public work keeps the external
  providers (on these two cores a public call would wait out the overflow timeout on every step and hold NEXUS from JARVIS's light calls).
- A call that may not overflow gets `private_timeout_seconds` (default 1200) instead of the 45 s overflow timeout; the NEXUS request's
  deadline follows the caller's context (no fixed 120 s).
- Private data still never leaves: nothing in this changes the fallback data classes.

## Consequences

- Private AURA tasks now finish on the owner's own server instead of failing; each model step takes from ~30 s (cached) to minutes (cold,
  long answers). They are slow, not interactive.
- Public tasks are unchanged (external free routes first).
- The owner can widen `primary_data_classes` (empty = every class) when NEXUS gets faster hardware.
