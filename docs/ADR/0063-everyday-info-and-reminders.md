# ADR-0063: Everyday information tools and reminders from conversation (JARVIS parity, wave 2)

Status: accepted (owner instruction of 2026-11-11, wave 2 of the plan in ADR-0062)

## Context
JARVIS answers weather, prayer times, Qur'an, places, routes and prices from public services, and keeps reminders
and daily routines (morning brief) that it delivers on time. AURA had none of these and no way to set a schedule
from conversation (only the API). The models were also never told the owner's local time.

## Decision
1. **`info.*` tools** (A0, read), each one fixed https host through the SSRF guard, no redirects, no key, answers
   are untrusted observations: `info.weather` (Open-Meteo), `info.prayer` (Kemenag schedule via api.myquran.com,
   with the next prayer), `info.quran` (equran.id: Arabic, transliteration, Kemenag translation, ≤30 verses),
   `info.place` (OpenStreetMap Nominatim), `info.route` (OSRM; car time with a stated typical-traffic factor, never
   called live traffic), `info.prices` (CoinGecko for crypto and gold via XAUT, ExchangeRate-API for currencies).
   Arguments are the planned `input` object and must match it at step time (same rule as connectors). A request
   without a place uses `home_city` (ASSUMPTION-0030). Granted by default with the other read tools.
2. **Reminders and daily schedules from conversation.** A reply may carry a `schedule` block: items label = what,
   value = `YYYY-MM-DDTHH:MM` (once, workspace time zone) or `daily HH:MM`, note = `remind` | `task`. The Core keeps
   reminders in `reminders` (migration 0017, idempotent per task) and the scheduler turns a due one into an owner
   notification (one-shot reminders switch off; a slot is claimed before notifying, so never twice). A `task` item
   with a daily time becomes an ordinary task trigger: each run is a normal task with the same grant, budget and
   approval rules. API: `GET /api/v1/reminders`, `DELETE /api/v1/reminders/{id}`. Failed tasks keep no schedule.
3. **The phone rings on time.** Android sets an alarm (`setAndAllowWhileIdle`, no extra permission) for each
   reminder due within 36 hours and shows it itself; the background check skips the server notification of a
   reminder the phone already showed (key `reminder-id@due-ms`).
4. **Local time in prompts.** The planner and the reply writer receive "Now" in the workspace time zone with the
   weekday, so "besok jam 7" resolves correctly.

## Alternatives
- Reminders as task triggers: the triggers table only allows `task|watch`, and a reminder must cost nothing and run
  no model; a separate table keeps that guarantee.
- Exact alarms (`SCHEDULE_EXACT_ALARM`): a special permission; inexact-while-idle alarms are accurate to minutes.

## Consequences
- Public services can be down or rate-limited; the tool fails visibly and the owner is told.
- The gold price is the XAUT token price, labelled as such, not a dealer's (e.g. Antam) buy-back price.
