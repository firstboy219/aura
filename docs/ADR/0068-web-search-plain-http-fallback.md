# ADR-0068 — web.search: a plain HTTP DuckDuckGo fallback

Status: accepted (owner instruction of 2026-11-14: "cek log JARVIS … perbaiki semua yang dia tidak bisa").

## Context
Tasks that needed the web failed verification again and again (hotel day-use price, job lists, news): `web.search` returned `provider: chromium, results: 0` twice per task, 20 s each.
Measured on 2026-11-14 with `aura browser check`: Bing answers the headless browser with a page of unrelated results (the ADR-0062 relevance check rightly refuses them) and
DuckDuckGo's HTML endpoint answers it with HTTP 403. The Wikipedia fallback has nothing for hotel prices. A plain HTTP POST to the same DuckDuckGo HTML endpoint from the same host
is answered normally (JARVIS has used it for its quick look-ups).

## Decision
`tools.DuckDuckGoHTML` (POST html.duckduckgo.com/html/, through the SSRF guard, results parsed with fixed patterns, ads skipped, redirect links unwrapped, https only) and
`tools.FirstWithResults` (the first searcher with results wins). The Chromium searcher's fallback is now `FirstWithResults{DuckDuckGoHTML, Wikipedia…}`. Results stay untrusted data;
nothing else changes (egress, approvals, the engines list).

## Consequences
Searches that the browser engines cannot serve are answered in about a second. DuckDuckGo may block this too one day; then the next fallback (Wikipedia) answers as before and the failure is visible in the trace.
Also added: guidance skill `jangan-klaim-aksi-tanpa-alat` (a claim of an action needs a tool that did it).
