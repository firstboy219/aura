# ADR-0082: JARVIS's features as a worker tool (jarvis.fitur)

- Status: accepted (owner request 2026-11-17: "buat AURA aware dan bisa menggunakan semua fitur yang sudah ada di JARVIS seperti fitur cek
  berita, Al-Qur'an, musik, dll")
- Date: 2026-11-17

## Context

In ODI, JARVIS presents and AURA acts. AURA's workers had their own public tools (web, news headlines, weather, the Qur'an text…) but could not
see or use what the owner keeps in JARVIS: today's news analysis ("Cek berita"), his Qur'an reader, his music and playlists, notes, money,
commute plans. The owner asked that AURA know and use all of them.

## Decision

A new read-class tool `jarvis.fitur` (policy A0, SideEffect "read"):

- The Core calls JARVIS's `POST /api/v1/aura/fitur` over the internal `aura-net` (`AURA_JARVIS_URL`, default `http://jarvis-core:8000`) with
  AURA's own service token (vault `connector.jarvis_tool_token`; JARVIS keeps the same value in `data/aura/jarvis_tool_token`, 0600). It is not
  the owner's device token and is never printed or logged.
- Input `{fitur, aksi, data}` is planned like an info tool (the planned input must equal the call). `aksi = "daftar"` lists a feature's actions.
- JARVIS runs the same action table as its chat (`aksi.py`): a READ returns its data; any action that changes something becomes an approval
  in JARVIS's "Izin & persetujuan", approved by the owner with the phone's lock. A worker reads untrusted pages, so its word never changes the
  owner's things — the same rule JARVIS applies to a chat turn that read something written by others.
- The answer is an observation marked as data, not instructions. Every call is audited on both sides.

## Alternatives

- Copy JARVIS's features into AURA tools: duplicates logic and data, and the two drift apart.
- Let workers run JARVIS actions directly: rejected — an injected page could then act on the owner's data.

## Consequences

- AURA now depends on JARVIS being reachable for this one tool (a failure is a tool failure, reported honestly); every other AURA path is
  unchanged. This is an ODI-level dependency the owner asked for, recorded here as the exception to AGENTS.md rule 9 for this tool only.
- New JARVIS features become available to workers without an AURA change (the catalog comes from JARVIS).
