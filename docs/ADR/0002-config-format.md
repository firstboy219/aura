# ADR-0002 — Configuration format and secrets loading

Status: **Proposed** — awaiting owner approval (P0 gate). Date: 2026-11-09.

## Decision
- **YAML** (`gopkg.in/yaml.v3`, decoding with `KnownFields(true)`) at `/etc/aura/config.yaml`, plus environment
  overrides `AURA_<SECTION>_<KEY>` for scalar values only. Invalid or unknown keys prevent startup (§13).
- Config holds **paths to secrets, never secrets**: `server.token_file`, `providers.openrouter.key_file`. Secret
  files must be regular files, not symlinks, mode ≤ 0440, owned by the Core user; otherwise startup fails with a
  message that names the path but never the content.
- Config is versioned (`config_version: 1`); the effective config (with secret paths, without values) is logged at
  startup and hashed into each task's `TASK_CREATED` event for reproducibility.

## Alternatives
TOML (fine, less familiar to the owner); JSON (no comments). YAML chosen for readability of budgets/models blocks.
