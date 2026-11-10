# ADR-0052 — Android client v0.1 (P7)

Status: **Proposed** (P7, 2026-11-10). The owner chose "build directly with Gradle". The toolchain is AURA's own
(`deploy/android.Dockerfile`): JDK 17, Android cmdline-tools 11076708 and Gradle 8.10.2, all pinned by sha256. Nothing from
another product's build is read or reused (Blueprint §15).

## Decision
- **Client of the public API** (AGENTS rule 17). Kotlin, Android 8.0+ (minSdk 26, target 35).
  - No third-party runtime libraries: only the Android platform and the Kotlin standard library (Apache-2.0). JUnit and
    org.json are test-only.
  - Screens: tasks (create, view, answer AURA's questions), approvals (shows the exact action and binds the approval to its
    hash), notifications, settings.
- **Credential.** A *member* token, never the owner's master token. The owner creates it with
  `aura admin member add ponsel approver` and can disable it at any time.
  - It is stored encrypted with a non-exportable Android Keystore AES-GCM key.
  - No cloud backup or device transfer; HTTPS only; system CAs only; redirects are not followed, so the token is never sent
    elsewhere.
- **Sensors (D.1 P7).**
  - The microphone is **off by default**. It runs only when all of these hold: the owner turned it on in the app, Android
    permission is granted, the app is in the foreground, and the owner tapped 🎤.
  - A red banner with a "Stop" button is visible the whole time it is on, and Settings has "Stop all sensors". It stops
    when the app leaves the foreground.
  - Speech recognition is Android's on-device recognizer, offline preferred. It returns text only: AURA records and stores
    no audio. Speech output uses the device's text-to-speech. So no external voice provider is needed.
  - The camera and screen are not requested in the manifest at all. Visual perception needs a vision-model decision.
- **Background behaviour.** Optional, using JobScheduler. Android decides when it runs (at most every 15 minutes, under
  Doze and battery saver). There is no always-on service and no sensor use in the background. It posts a notification for
  new notices and pending approvals.
- **Release.** `sudo deploy/android-build.sh <version>` runs unit tests, lint (abort on error) and the release build, then
  signs with the AURA keystore and verifies with `apksigner`. The keystore is RSA 4096, in `/opt/aura/release/android`,
  mode 0400, created on first use; its password is passed as a file, never as an environment value. The output goes to
  `/opt/aura/releases/android/aura-<version>.apk` with its `.sha256`.

## Evidence (0.1.0, 2026-11-10)
- Unit tests 3/3: https-only server URLs, API shapes, microphone rules (each refusal reason, idempotent stop).
- Lint: no issues.
- APK: 655,605 bytes, sha256 `850bb932…c9c4e903`, signer certificate SHA-256 `9d4d2070…a9a6366`.
- Not yet run on a device: no emulator on the host. Manual check by the owner is the next step.

## Limits
- Approving from the phone uses only the member token (approver role). There is no second factor like the web's TOTP
  step-up yet. Mitigations: the member can be disabled, and the approval is still bound to the exact action hash.
- No push notifications (FCM would add a Google dependency). Polling under JobScheduler is used instead.
- No camera or screen perception; no live voice conversation, only dictation and read-out.
