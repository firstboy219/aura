# ADR-0061: Workspace — an empty home that fills with what AURA is doing

Status: accepted (owner instruction of 2026-11-11: home is an empty workspace plus the panel to talk to AURA; news
appears as headline cards while AURA explains, an analysis draws the people and things it is about, "note this"
opens the note list and writes into it, "play a song" opens a music player with its queue)

## Context
ADR-0060 made the home screen a page AURA designs, and narrated replies ran on a separate full-screen overlay. The
owner wants the opposite default: nothing on screen until something is asked, and then the answer appearing piece
by piece on that same surface, in step with what AURA says.

## Decision
1. **Home is the workspace.** The Android home shows an empty canvas and a communication panel (menu, keyboard,
   microphone orb, camera, stop). The talk overlay is removed: listening, the heard words, the caption and the
   reply's blocks all live on the workspace. A reply asked from the workspace is shown there block by block
   (`Narration.beats`), spoken when the owner enabled spoken results, otherwise with timed captions. The page AURA
   designs (ADR-0060) stays, as "Papan", one tap away.
2. **Three block types** join the view language (`internal/view`), sanitised like the rest:
   - `figures` — the people, organisations and things an explanation is about: `label` (name), `value` (what
     happened), `note` (what it led to). The **Core** looks each name up (`tools.WikiPictures`: the lead image of
     the matching Wikipedia page, two fixed hosts through the SSRF guard, no redirects, bounded size, image types
     only) and stores the picture by content hash in `media`; the item then carries `image` (the hash) and `url`
     (the source page). A model never supplies a picture address. No picture found → the client shows initials.
     Lookups are remembered in `media_lookups`, bounded in time per reply, and a failure never fails the reply.
   - `note` — items are notes to keep. The Core saves them (table `notes`, idempotent per task and position) when
     the reply is built; clients show the owner's note list with the new ones on top. Notes are the owner's own
     data in AURA's database: `GET/POST /api/v1/notes`, `PATCH/DELETE /api/v1/notes/{id}`.
   - `player` — `text` is what was asked for, `items` optional titles/artists AURA named. It carries no audio and
     no address. The phone matches it against **its own** music library (`Tunes.pick`) and plays from there.
   `links` blocks are drawn as headline cards (title, note, site); the provenance rule for links is unchanged.
3. **Pictures are served by the Core**: `GET /api/v1/media/{sha256}` under the normal token. Migration 0016 adds
   `notes`, `media`, `media_lookups`.
4. **Music stays on the phone.** The client reads MediaStore (permission `READ_MEDIA_AUDIO`, asked only when the
   player is first used), plays `content://media/` URIs only, and never sends the library to the Core. Playback
   starts by itself only after the narration ends and only when the library matched the request; otherwise the
   player says why (no permission, empty library, nothing matching) and offers the library.
5. **Prompt rules** (worker): when to use `figures`, `note`, `player`. Whether a song plays or a picture exists is
   the client's and the Core's fact, not the model's.

## Alternatives
- Let the model supply image URLs: rejected (untrusted addresses rendered on the owner's device; rule 6).
- Stream music from an online service: needs an account, a licence and a new egress class; not done here.
- Keep notes as memories: memories are AURA's knowledge with provenance rules; a note list is the owner's own
  list that they tick and delete, so it has its own table.

## Consequences
- Not done, and shown honestly in the app: the camera button is dimmed and says vision is not available (no route
  that would look at a photo without a provider that may keep it); online streaming; playback continuing after the
  app is closed (no foreground media service); pictures from sources other than Wikipedia.
- A picture lookup sends the figure's name to Wikipedia as a search term. It therefore runs only for tasks whose
  grant already includes `web.search` — the same class of egress the task was allowed anyway — and never
  otherwise.
- The web app renders the three new blocks.
