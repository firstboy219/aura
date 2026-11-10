# PROPOSALS.md — frozen-scope ideas (Blueprint §0 change freeze; AGENTS rule 21)

New capabilities, engines, integrations or scope additions are recorded here instead of being implemented. Template:

```
### PROPOSAL-0001: <title>
- Date / author:
- Problem:
- Proposal:
- Blueprint sections affected:
- Risk / cost:
- Status: proposed | accepted | rejected
```

### PROPOSAL-0001: Dedicated AURA provider key
- Date / author: 2026-11-09, Claude Code (checkpoint v1.3.11)
- Problem: the OpenRouter key in use is a copy of a key owned by another product (ASSUMPTIONS A-04), which Blueprint §15 and AGENTS rule 9 do not allow; spend and exposure are mixed.
- Proposal: the owner creates a key for AURA alone (own credit limit) and replaces `/opt/aura/secrets/openrouter_key`; AURA's daily budget then bounds a dedicated account.
- Blueprint sections affected: §15 (conformance, no change to the text).
- Risk / cost: none beyond creating the key.
- Status: **rejected by the owner 2026-11-10** — the key is shared on purpose (ASSUMPTIONS A-04).

### PROPOSAL-0002: Task-class definition beyond the tool set
- Date / author: 2026-11-09, Claude Code
- Problem: the Limitation Ledger groups tasks by the sorted tool set (a deterministic v1 heuristic); two very different jobs with the same tools share a class and dilute rates.
- Proposal: add an owner-visible task class (label chosen at creation or derived by the `fast` tier with a deterministic confirmation) in a later P3 milestone.
- Risk / cost: small schema addition (`task_class` already exists); needs an owner decision on labelling.
- Status: proposed
