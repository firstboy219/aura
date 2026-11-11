# ADR-0057: Live voice conversation on the Android client

Status: accepted (owner request of 2026-11-11: "buat aura bisa mendengar suara live owner dan memprosesnya secara live juga")

## Context
ADR-0052 gave the Android client dictation (one press, one sentence into a text box) and read-out. The owner wants to
talk to AURA: be heard live and answered live. The blueprint's Owner Conversation Agent (a realtime provider such as
Deepgram or ElevenLabs) is deferred by the owner and needs keys and a privacy decision, so it is not used here.

## Decision
- A live conversation mode in the Android client, built only on the device's speech recognizer and text-to-speech
  (no new dependency, no external voice provider, no change to the Core or its API):
  listen → the owner's words are shown while they are spoken (partial results) → at the end of the turn the text is
  sent as a normal task → the task is polled every 0.8 s → the reply text is spoken → listen again.
- Turn-taking is pure logic in `LiveTalk` (unit-tested); `Voice` owns the recognizer and speech; `MainActivity` wires
  them to the public API.
- Rules (Blueprint voice rules, D.1 P7 sensor rules):
  - Voice is an input channel, never an authorization channel. A turn is sent only as a new request or as the answer
    to a clarification question (which grants nothing). When a task reaches WAITING_APPROVAL the conversation ends
    and the owner is sent to the Approvals tab; no spoken word approves anything.
  - A transcript the recognizer reports with low confidence (< 0.35) is not sent; AURA asks the owner to repeat.
  - Partial transcripts are display only.
  - The microphone runs only after the owner pressed the button, with consent in Settings, the OS permission and the
    app in the foreground; each turn re-checks all three. The banner and its stop button stay visible for the whole
    conversation. Leaving the app, four silent listening windows in a row, or saying "berhenti" ends it.
  - The microphone is off while AURA speaks (it would hear itself); "Sela" cuts the speech short and listens.
  - No audio is stored or sent by AURA; only text reaches the Core. Tasks use the same private/public choice as the
    ask box (private by default).

## Alternatives
- Realtime provider (full duplex, barge-in by voice, better recognition): deferred by the owner; needs keys, cost and
  a data-processing decision. Remains the OCA of the blueprint.
- Browser speech recognition in the Web App: the browser sends audio to its vendor, outside AURA's control; the Web
  App keeps `Permissions-Policy: microphone=()`.

## Consequences
- Latency is recognizer end-of-speech + a normal task + speech. It is turn-based, not full duplex.
- Recognition quality and offline availability depend on the phone's recognizer and its Indonesian language pack.
- Evidence: `ClientTest.liveConversationTakesTurns`, `ClientTest.liveVoiceNeverApprovesAndDoubtfulWordsAreNotSent`
  (implementation-verified for the logic; the microphone path itself is device behaviour and was not run on a
  phone in the build environment).
