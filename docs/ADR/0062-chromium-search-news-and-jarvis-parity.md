# ADR-0062: Web search through Chromium, news without keys, background browsing, and the JARVIS parity plan

Status: accepted (owner instruction of 2026-11-11: "Brave ganti ke Chromium aja dan pakai juga playwright untuk
pekerjaan background dan tolong replicate semua fitur di Jarvis ke aura kecuali layout apk & hermes, karena Hermes
penggantinya adalah aura")

## Context
`web.search` had two providers: Wikipedia (keyless, encyclopedic only) and Brave (needs a paid key the owner does
not want). "Cek berita hari ini" therefore had no real headlines. AURA already runs Playwright with headless Chromium
in the isolated `aura-browser` container (ADR-0039), whose only way out is the Core's egress proxy. The owner also
asked that AURA take over every JARVIS feature except the APK layout and Hermes, which AURA replaces. Earlier
approval (2026-11-10) allows re-implementing JARVIS features as concepts; no JARVIS code is copied and AURA has no
runtime dependency on JARVIS (AGENTS rule 9).

## Decision
1. **`web.search` provider `chromium`** (new default in the deployed config). The Core opens a fresh browser session
   per search whose egress credentials reach only the engine's own domains (`*.bing.com`, then
   `html.duckduckgo.com`), loads the result page, and the browser host reads the results with **fixed selectors**
   (`page.serp`; no page JavaScript chosen by anyone, no CDP). Engine redirect links are unwrapped by the Core and
   only plain absolute http(s) URLs without credentials survive. The session is closed after each search. If every
   engine refuses or finds nothing, Wikipedia answers. Measured on this host on 2026-11-11: Bing served results to
   Chromium; DuckDuckGo answered 403 (kept as second engine because it may recover). Config:
   `tools.web.search: {provider: chromium, engines: [bing, duckduckgo], region: ID}`. Brave stays supported but is
   no longer configured.
2. **`news.headlines` tool** (A0, read). The Core reads RSS from a fixed list of outlets (ANTARA, CNBC Indonesia,
   CNN Indonesia, Tempo, Republika, BBC Indonesia, BBC World, Al Jazeera, CNBC) and Google News (Indonesian
   edition; topic search when a topic is given), through the SSRF guard, without redirects to other hosts, size
   bounded, no model call. Items older than 36 hours are dropped from general headlines; outlets that did not answer
   are named in the observation. Headline links are recorded as provenance (ADR-0017). The worker uses the tool for
   news questions and shows headlines as `links` cards (ADR-0061).
3. **`tools.web.fetch.follow_results`** (default false; set true in the deployed config on the owner's
   instruction). A URL the Core itself recorded for the task — a search or news result, or a link of a page it
   fetched — may be fetched although its site is not in `allow_domains`; redirects stay on that site; the SSRF guard
   still checks every connection; a URL without provenance is still refused. Without this the owner's tasks could
   find pages but never read them.
4. **Background work in Chromium.** A watch trigger may set `spec.render: browser`: its sources are read in a short
   browser session limited to the watch's domains (pages that only render with JavaScript). Scheduled tasks already
   may use the browser tools under their grant.

### Security notes
- A search query leaves AURA to Bing/DuckDuckGo exactly as it did to Brave; the result page is untrusted data.
- `follow_results` widens what a task may *read*, never what it may write or send. Exfiltration through a URL needs a
  URL composed by the model, which provenance refuses.
- The plan contract enum in `contracts/schemas/task.plan.json` was already behind the runtime tool set (browser
  tools); `news.headlines` follows the same path and the enum will be regenerated with the next contract version.

## JARVIS parity plan
Inventory taken read-only from the JARVIS repository on 2026-11-11 (no secrets read). Each wave is shipped, tested
and deployed before the next.

| Wave | Features (JARVIS concept → AURA) | Notes |
|---|---|---|
| 1 (this ADR) | Search without keys, news brief data, reading result pages, background browsing | done |
| 2 | Reminders and schedules from conversation; morning news brief and evening recap; weather (Open-Meteo); prayer times and Hijri date (Kemenag schedule); Qur'an text/translation; maps, geocoding, routes and ETA (Nominatim/OSRM); price watchers (crypto, gold, currency) | keyless public APIs, A0 read tools, notifications only |
| 3 | Documents: create Word/Excel/PDF/Markdown; read attachments (PDF, Word, Excel); PDF tools (merge, split); translation; video summaries from captions; cinema schedules | files stay in the task workspace |
| 4 | Outlook mail and calendar (Microsoft Graph, device-code sign-in), unified "needs attention" triage, communication memory and digests, people and birthdays | every outgoing mail or event is an A2 approval |
| 5 | Phone-side features in the Android client: notification triage, places and habits, commute (KRL, routes), spending from notifications, situational awareness, alarm | phone permissions asked one by one |
| Owner decision needed | Phone control through accessibility (ordering rides, coffee, paying), investment trades, company federation, self-modifying dev team | money and code changes; JARVIS requires PIN/admin password; AURA rule 11 forbids modifying L4 |
| Not replicated | Hermes (AURA is its replacement); the APK layout; WhatsApp account linking (owner constraint); YouTube-to-MP3 conversion (licensing); n8n/Qdrant/Postgres as infrastructure (AURA keeps SQLite per the blueprint; the features are re-implemented natively) | |

## Alternatives
- A paid search API (Brave, SerpAPI): rejected by the owner.
- Scraping search results over plain HTTP: DuckDuckGo and others answer bots with 202/403; Chromium passes and
  stays inside the existing browser isolation.
- Letting the model run JavaScript to extract results: forbidden (AGENTS rule 5).

## Consequences
- Search depends on Bing tolerating a headless browser; when it stops, Wikipedia keeps `web.search` answering and
  the observation names the provider used.
- The browser container now carries search traffic; searches are serialized by the host (one command at a time).
