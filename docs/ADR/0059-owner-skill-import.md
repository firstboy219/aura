# ADR-0059: Importing the owner's skill library

Status: accepted (owner instruction of 2026-11-11: "impor skill jarvis")

## Context
The owner keeps a library of guidance skills (written for their JARVIS assistant, MIT, author = owner's project).
Blueprint §15 / AGENTS rule 9 forbid any dependency on another product; the owner explicitly approved copying these
texts (rule 13: copying with owner approval).

## Decision
- The texts are copied once into `skills/owner/` and adapted: front matter reduced to name/description/source, lines
  that name JARVIS-only tools (stage_show, n8n, cron jobs, create_document, show_route …) removed or rewritten for
  AURA's tools (`web.search`, `web.fetch`, `data.read`, `calc`, `workspace.write`, AURA triggers). AURA has no runtime
  link to JARVIS: nothing is read from it after the copy.
- Not imported (they are about JARVIS itself or its infrastructure): jarvis-flow-audit, jarvis-self-development,
  jarvis-self-repair, n8n-workflow-design, server-diagnostics, musik-dan-media, web-login-aman.
- New owner endpoint `POST /api/v1/skills/import` and CLI `aura skill import --stdin`: a *guidance skill* (notes up
  to 2000 characters, no plan template, no source task). It is versioned, can be disabled/rolled back like a learned
  skill, carries no permissions, and passes the same protected-topic screen.
