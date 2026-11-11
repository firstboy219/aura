# ADR-0058: Understanding the owner's speech

Status: accepted (owner request of 2026-11-11: "sempurnakan bagian owner bicara ke aura dengan level expert (termasuk
mengkoreksi kata jika suara owner tidak clear dan memahami maksud kata2 dari owner)"; AURA's own voice stays as in ADR-0057)

## Context
ADR-0057 sent only the recognizer's best transcript and dropped any turn under 0.35 confidence. Phone recognizers
mishear names, numbers, brand and English words in Indonesian speech, and the owner speaks informally. The words
reached the planner as if they were exact.

## Decision
- **Device:** the recognizer returns up to 5 hypotheses with confidences (`EXTRA_MAX_RESULTS`); the turn waits for a
  longer pause (1.8 s) before ending; Android 13+ formats numbers and punctuation and is biased towards the owner's
  vocabulary: words AURA's understanding added to what was heard (a corrected name, place, term), kept on the device
  only (max 100). No audio is recorded or sent; only text leaves the device, as before.
- **API:** `POST /api/v1/tasks` takes optional `voice.alternatives[]` (1..5, text ≤2000, confidence 0..1), stored in
  the task scope. The objective stays the recognizer's best text: the owner's words are never overwritten.
- **Planner:** for a spoken task it gets the alternatives and a rule to reconstruct what was said and meant (compare
  hypotheses, repair Indonesian recognition errors, numbers and times, sound-alike English/brand words, resolve names
  and references from the conversation, memory and profile, ignore fillers, never add anything), and returns
  `heard_as`. If two readings stay plausible and lead to different actions, it asks one short confirmation
  ("Maksud Tuan: …?"); for a conversational task that question *is* the reply (note `voice_confirm`), nothing is done.
- **Core:** `heard_as` is content, so it is kept in a `heard` checkpoint; the timeline gets `VOICE_UNDERSTOOD` with
  metadata only (`corrected`, number of alternatives). Plan review, steps, replan, verification, reflection and the
  reply all receive the understood objective plus the raw transcript. `GET /tasks/{id}` returns `heard_as`; the app
  shows "🎧 Saya tangkap: …" when it differs.
- **Typed input:** the planner is told that informal Indonesian (abbreviations, slang, typos, mixed language) is to be
  understood, not questioned. Typed objectives are never reinterpreted (`heard_as` is ignored without voice input).
- **Doubt:** below 0.35 a turn is sent only if there are other hypotheses for AURA to reconcile (it then confirms);
  below 0.15, or as the answer to AURA's question, a doubtful turn is never sent ("Maaf, kurang jelas").

## Unchanged boundaries
Voice is an input channel, not an authorization channel: understanding grants nothing, approvals are still given by
hand against the action hash, and a WAITING_APPROVAL ends the conversation (ADR-0057). AURA's spoken output is unchanged.

## Alternatives
- A separate "understand" model call before planning: one more round trip per turn, slower live conversation.
- A cloud speech provider with custom vocabulary: deferred by the owner; needs keys and a privacy decision.

## Consequences
Contract `task.plan` gains `params.voice` and `result.heard_as` (both optional, additive). Not tested on a physical
phone; recognizer extras are hints that some devices ignore.
