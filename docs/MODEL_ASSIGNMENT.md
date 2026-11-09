# AURA — MODEL ASSIGNMENT

| Field | Value |
|---|---|
| Version | 1.2 |
| Baseline | MASTER_BLUEPRINT v1.3.11 |
| Owner | Product Owner |
| Status | Owner-provided default. Values are hypotheses until benchmarked (RESEARCH_BENCHMARK.md). |

This document answers two questions:

1. **Runtime:** which Claude model AURA uses for each kind of work it does (Section 2).
2. **Build:** which Claude model Claude Code (the sole implementation agent) should use for each kind of implementation task (Section 3).

**Model identity contract (Blueprint Section 8.5.1).** Four values are kept separate and never interchanged: the logical tier (`fast`, `standard`, `deep`), the display name (for example "Claude Sonnet 5.5"), the provider's canonical API model ID (for example `claude-sonnet-5-5`), and the router-specific route ID (for example an OpenRouter route). This document gives display names and API IDs; route IDs are verified and recorded at P1. This document is a default hypothesis, not proof that a route works.

AURA stays provider-independent (Blueprint Section 3.1). This is the default **profile**, not a dependency. Agent logic refers only to tiers (`fast`, `standard`, `deep`); this file maps tiers to models.

Current Claude models referenced: **Claude Haiku 5.5** (`claude-haiku-5-5`), **Claude Sonnet 5.5** (`claude-sonnet-5-5`), **Claude Opus 5.5** (`claude-opus-5-5`), **Claude Fable 5.1** (`claude-fable-5-1`). Exact route IDs through OpenRouter, availability, prices, and parameter names (extended thinking / effort) must be verified at P1 and recorded in an ADR.

---

## 1. Tier Mapping (profile `claude-default`)

| Tier | Default model | Reasoning effort | Why |
|---|---|---|---|
| `fast` | Claude Haiku 5.5 | low | Cheapest and fastest; good for narrow, well-specified work |
| `standard` | Claude Sonnet 5.5 | medium | Main workhorse: strong planning and tool use at moderate cost |
| `deep` | Claude Opus 5.5 | high | Hardest reasoning, escalation, high-stakes verification |
| `deep` candidate | Claude Fable 5.1 | high | Evaluate in the P2 benchmark against Opus 5.5 on INT-X1 and hard planning cases. Adopt only if it wins on quality per cost. It carries additional safeguards for cybersecurity, biology, and LLM R&D topics, so do **not** use it for security testing, adversarial fixture generation, or self-evolution work (L2/L3) unless benchmarks show no interference. |
| `embedding` (P3) | Non-Claude embedding provider | — | To my knowledge Anthropic does not offer an embedding model; choose a provider by ADR in P3 |
| Speech recognition, OCA (P5) | Non-Claude STT: Deepgram Nova-3 (`id`, multilingual) as primary evaluation candidate | — | Flux is not used for Indonesian sessions (its multilingual list excludes Indonesian at writing); re-verify the provider matrix at P5 |
| Speech synthesis, OCA (P5) | Non-Claude TTS: ElevenLabs streaming as primary evaluation candidate; alternatives behind the same interface | — | Voice and model IDs live in configuration, never in business logic |
| Reasoning behind voice (P5) | Claude via the same tiers (`standard`, escalate to `deep`) | — | OCA passes structured utterances to the main agent; Claude never receives raw audio |

**Fallback profile (required by P2a):** a second provider mapped to at least `fast` and `standard`, used on provider outage or when a workspace's data-route policy forbids the primary provider.

---

## 2. Runtime: Which Model for Which Task

| AURA activity | Tier | Default model | Phase | Notes |
|---|---|---|---|---|
| Intent and task classification | fast | Haiku 5.5 | P1 | Short structured output |
| Planning (ordinary tasks) | standard | Sonnet 5.5 | P1 | Structured plan with criteria |
| Planning (complex / multi-system / A2 actions) | deep | Opus 5.5 | P2 | Triggered by planner complexity flag |
| Plan review pass (Section 8.7) | deep (or a different tier from the planner) | Opus 5.5 | P2 | Only for complex plans |
| Step execution decisions | standard | Sonnet 5.5 | P1 | Tool choice and arguments |
| Summarizing fetched pages (RS-1) | fast | Haiku 5.5 | P1 | Untrusted content stays labelled |
| Extraction from documents and data (`data.read` output) | fast | Haiku 5.5 | P2 | Escalate to standard if verification fails |
| Drafting reports and long-form output | standard | Sonnet 5.5 | P2 | |
| Business and strategic analysis (INT-02, INT-08, INT-X1) | deep | Opus 5.5 | P2 | Numbers always via `calc` |
| Deterministic checks | — | none | P1 | Code, not a model |
| Rubric verification (first pass) | fast | Haiku 5.5 | P2 | |
| Verification escalation / high-stakes final check | deep | Opus 5.5 | P2 | After two failures or for A2 outcomes |
| Context compaction / summaries | fast | Haiku 5.5 | P2 | |
| Main agent integration of subagent findings | standard → deep | Sonnet 5.5 / Opus 5.5 | P2 | Deep when findings conflict |
| Subagent work (research, drafting) | standard | Sonnet 5.5 | P2 | |
| Quarantined subagent reading untrusted content | fast | Haiku 5.5 | P2 | Read-only capabilities |
| Clarification question generation (`input.request`) | standard | Sonnet 5.5 | P2 | |
| Reflection and lesson candidates | fast | Haiku 5.5 | P3 | |
| Skill distillation (L1) | standard | Sonnet 5.5 | P3 | |
| Memory promotion check | standard | Sonnet 5.5 | P3 | |
| Self-model narrative ("what can I do?") | fast | Haiku 5.5 | P3 | Facts come from the registry, not the model |
| Charter classifiers (e.g. fatwa-request detection) | fast | Haiku 5.5 | P3 | Rule action is deterministic in Core |
| Religious-knowledge answers | deep | Opus 5.5 | P3 | Citations must resolve against approved corpus |
| L2 tool generation (code + tests) | deep | Opus 5.5 | P4 | Not Fable (see Section 1) |
| L3 brain improvement proposals | deep | Opus 5.5 | P6 | Evaluated in AURA Lab |
| Live Agent reasoning behind voice | standard | Sonnet 5.5 | P5 | Voice I/O by realtime provider |
| Enterprise connector reasoning | standard | Sonnet 5.5 | P5+ | Deep for cross-system plans |
| Short-reply interpretation against dialogue state (OL-09) | fast | Haiku 5.5 | P2a | "ya / setuju / maksudnya?" resolved against the last question |
| Persistent Supervisor while idle | — | none | P2b | Event-driven Go code; no model call while idle (PERF-13) |
| Woken-task re-planning after a dependency change | standard | Sonnet 5.5 | P2b | Deep if the plan changes materially |
| Activity timeline | — | none | P2b | Derived from Core events, not model-authored |
| Owner Working Profile: inferring a tentative preference | fast | Haiku 5.5 | P3b | Stays tentative until confirmed |
| Trace-driven router | — | none (statistical policy) | P3b | Shadow mode first; no LLM in the routing decision |
| Proactive research: change detection and summary | fast | Haiku 5.5 | P3b | Read-only; escalate to standard for relevance judgement |
| Browser action decisions (choose element, fill form) | standard | Sonnet 5.5 | P4 | Page content is untrusted; A2 for side effects |
| Browser page reading / extraction | fast | Haiku 5.5 | P4 | Escalate on verification failure |
| OCA utterance normalization, intent hint, ambiguity flags | fast | Haiku 5.5 | P5 | Optional, only if measured to help; never rewrites meaning |
| Limitation detection (Section 8A.5) | — | none (statistics over traces) | P3a | Thresholds and minimum samples; no model |
| Limitation diagnosis | standard | Sonnet 5.5 | P3a | Deep for suspected AURA defects or brain-level causes |
| Remedy planning (R1–R3) | standard | Sonnet 5.5 | P3a | R3 tool code generation uses deep (L2 row) |
| R4 brain-remedy proposals | deep | Opus 5.5 | P6 | Evaluated in AURA Lab |
| Honest limitation disclosure text | fast | Haiku 5.5 | P3a | Numbers come from the ledger, not the model |

The self-model (P3) records success, cost, and latency per activity and tier. Mappings change only through benchmark evidence and an ADR, never ad hoc.

---

## 3. Build: Which Claude Model for Which Implementation Task (Claude Code)

| Implementation task | Model | Why |
|---|---|---|
| Phase 0 architecture documents, ADRs, threat model, data model | Opus 5.5 | Highest-consequence design decisions |
| Security-critical code: capability grants, approval hashing, SSRF guard, path safety, budget reservations, idempotency and recovery, secret handling, sandbox, WASM host functions | Opus 5.5 | Subtle correctness and security |
| State machine, event log, migrations, single-writer queue | Opus 5.5 (design) → Sonnet 5.5 (implementation) | Design once carefully, implement efficiently |
| Ordinary implementation: API handlers, CLI, worker logic, Gateway adapter, tools `data.read`/`calc`, Console TUI, Web UI | Sonnet 5.5 | Best quality per cost for routine code |
| Persistent Supervisor, leases, wake deduplication | Opus 5.5 (design) → Sonnet 5.5 (implementation) | Durability and concurrency edge cases |
| Browser runtime isolation, egress proxy, session/credential handling | Opus 5.5 | Security-critical |
| OCA session gateway, turn manager, voice-approval flow | Opus 5.5 (design and approval flow) → Sonnet 5.5 (adapters) | Approval safety and streaming correctness |
| Model registry, trace/telemetry schema, adaptive-routing shadow harness | Sonnet 5.5 | Data-heavy, moderate risk |
| Limitation Ledger, detection thresholds, remedy routing, re-test/rollback harness | Opus 5.5 (design) → Sonnet 5.5 (implementation) | Must never weaken gates or widen permissions |
| Unit tests, fixtures, synthetic datasets, scenario YAML | Sonnet 5.5 (Haiku 5.5 for bulk fixture variations) | |
| Adversarial security tests (SEC-X1, injection families) | Opus 5.5 | Do not use Fable 5.1 (safeguards may interfere) |
| Documentation, README, changelogs, lint fixes, formatting | Haiku 5.5 | Low risk, high volume |
| Benchmark runners and report generator | Sonnet 5.5 | |
| **Independent review** of every security-critical PR | Opus 5.5 in a **separate fresh session** (not the author session) | Work must not grade itself |
| Hard architectural problem or stuck debugging | Opus 5.5; optionally Fable 5.1 for non-security reasoning | |

Rules:
- `IMPLEMENTATION_SPEC.md` lists a **recommended model per milestone** using this table.
- The completion report states which model(s) were actually used.
- Cost-saving switches (for example Opus → Sonnet for a security-critical milestone) require owner approval.

---

## 4. Benchmark Judge Models (model-graded evaluation)

| Use | Model | Rule |
|---|---|---|
| Rubric scoring of MODEL_EVAL tests | Opus 5.5, pinned version | Deterministic checks first; judge sees rubric and evidence, not which agent produced the output |
| Agent comparisons (AURA vs Hermes/OpenClaw/OpenJarvis) | Opus 5.5 + a non-Claude judge on a sample | Blind, randomized order; report judge agreement |
| Voice quality (naturalness, prosody, pronunciation) | Human review (blinded) | Models are not used as the only judge for speech quality |
| Human review | Owner or expert | At least 10% sample of MODEL_EVAL results and all disagreements between judges |

If the system under test uses Opus 5.5 for the graded output, use a second judge from another provider for that test family and report both.
