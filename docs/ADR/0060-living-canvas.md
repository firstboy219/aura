# ADR-0060: Living canvas — richer views, narrated explanations, AURA-designed screens, Compose client

Status: accepted (owner instruction of 2026-11-11: rebuild the app's technology and design; make the display follow
what AURA says; let AURA change the display itself; adopt the owner's most-used assistant features)

## Context
The Android client (ADR-0052) was a fixed View-based app: the same screens whatever was happening, a reply shown as
a static card, and no way for AURA to adjust the app without a new build. The owner asked for a display that changes
with context, supports AURA while it explains (spoken and shown in step), and that AURA can redesign on request.
ADR-0052 also ruled out third-party UI libraries.

## Decision
1. **View language v2** (`internal/view`). Six more block types every client implements — `hero`, `timeline`,
   `progress`, `compare`, `quote`, `form` — and four optional per-block fields: `say` (the sentence spoken while the
   block is shown), `icon` (closed list), `span` (full/half) and `emphasis` (normal/strong/quiet). A reply may carry a
   **scene** `{mood, accent, layout}`, each from a closed list. Everything passes the same Core sanitiser as before:
   unknown values are dropped, sizes are bounded, numbers stay grounded, links need provenance.
2. **Narration.** The client plays a reply as beats: each block is revealed and highlighted while its `say` sentence
   is spoken (device TTS). `say` is text only; it cannot trigger anything. The `spoken` flag marks replies composed
   for voice.
3. **Forms and actions are proposals.** A `form` block's request template and an `actions` item are placed in the
   owner's composer (or sent only by the owner's own tap on "send"); a model can never submit a task by itself, and
   the resulting task goes through the normal grant/budget/approval path.
4. **AURA-designed screens.** A screen is *data*: a scene plus blocks, where a `widget` block names a live-data
   source from a closed list (greeting, ask, attention, activity, recent, approvals, notifications, suggestions,
   skills, memories, schedules, limitations, self, usage). The client fills a widget from the public API under its
   own token — a widget carries no URL, query or code. A task with `design: <screen>` runs the new worker method
   `ui.design` (no tools); the Core sanitises the result and stores it in `ui_screens` (migration 0015) as a new
   version. Endpoints: `GET /api/v1/ui/home`, `GET /api/v1/ui/screens/{name}`, `POST /api/v1/ui/screens/{name}/
   {rollback|reset|use}`. Every version is kept; rollback, reset to the built-in default and "use version N" are
   one call. No model output is ever evaluated or executed (AGENTS rules 5 and 6): "AURA codes its display" means it
   composes this bounded declarative language, not program code.
5. **Home context is computed by the Core** (`Engine.Home`): time of day in the workspace time zone, what is
   waiting, what is running — deterministic, no model call.
6. **Gateway null-fill.** For strict structured output, optional fields missing from a model's answer are filled
   with `null` before schema validation, so one omitted optional field does not cost a repair call.
7. **Android client on Jetpack Compose** (Apache-2.0; BOM 2024.12.01, Kotlin 2.0.21), with Paparazzi (Apache-2.0,
   test-only) for screenshot tests. This supersedes the "no third-party libraries" line of ADR-0052 for the UI
   toolkit only; networking, storage and voice stay on platform APIs. R8 stays on; the APK is about 1.2 MB.
8. **Adopted features.** With the owner's approval, features the owner uses most in their other assistant are
   re-implemented here as concepts — one-tap suggestions (briefing, recap, news, research, analysis, career,
   story, code, explain, change the look), a talk screen, read-aloud. No code was copied and nothing calls that
   product (rule 9 holds at runtime). Features that need capabilities AURA does not have yet (camera/OCR, phone
   control, music, maps, live meeting translation, prayer times, commute, money, habits) are *not* claimed; they
   are listed as a ranked roadmap in the completion report.

## Alternatives
- Server-rendered HTML/WebView or model-written UI code: rejected — executing model output breaks rules 5/6.
- Keep Views and add block types only: does not give animation, adaptive layout or screenshot tests at a
  reasonable cost.
- A per-client layout config file edited by the owner: does not meet "AURA changes the display on request".

## Consequences
- A design mistake by a model costs at most a dropped block; the built-in default screen is always one call away.
- Clients must ignore unknown block types and fields (the web app renders the new types too).
- Voice remains an input channel, never an authorization channel; narration stores no audio.
- The view contract grew (`task.reply`, `task.plan`, envelope, new `ui.design`); contract tests cover both sides.
- Two more build-time dependencies on the Android side (Compose, Paparazzi), both Apache-2.0.
