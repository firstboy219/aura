# ADR-0040 — Browser login, one-time codes, downloads and uploads

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
quarantined, uploads off by default) and D.1 P4 ("browser authentication uses a human-mediated or approved delegated flow; no raw
passwords, cookies or MFA secrets are exposed to the model/worker; CAPTCHA/MFA challenges are handed back to the human").

## Decision
- **Login profiles** live in the config (`browser.logins`): name, domains, username, and `password_ref`. `password_ref` is a
  `vault:<name>` reference or a 0400 file. A task may use a profile only if the owner names it at submission
  (`--browser-profile`).
- **`browser.login`** is A2. The model picks only the three inspected fields (username, password, sign-in button). Before typing,
  the Core requires:
  - the profile is the task's own profile;
  - the page is `https` on one of the profile's domains;
  - the password field is a real `<input type=password>`;
  - the owner's approval, bound to the page and the three elements, and a re-check that the login page is unchanged.

  The Core then reads the credentials and types them, and clicks the button. Credentials never reach the worker, the model, the
  trace or an approval.
- **One-time codes:** `browser.mfa`, allowed only after a successful login in the same task, parks the task on the event
  `mfa:<task>` and notifies the owner. `aura browser code <task> <code>` (`POST /tasks/{id}/browser-code`) types the code
  straight into the page, clicks the confirm button, and wakes the task. The wake event carries no code. The code is not stored.
  CAPTCHA is never solved: it stops the task as `SITE_BLOCKED`.
- **Saved sessions:**
  - After a login or a code, the session's cookies and storage are exported, filtered to the profile's domains and stored
    AES-GCM encrypted in the vault (`browser-session.<profile>`).
  - Later tasks of that profile start logged in.
  - `aura browser session import <profile> --file F` accepts a Playwright storage state or a cookie-list export from the owner's
    own browser. This is the human-mediated route for sites that sign in with Google; cookies of other domains are dropped.
  - `aura browser session delete <profile>` forgets the session.
- **Google accounts:** AURA does not type Google passwords and does not try to pass Google's automated-browser checks
  (rule 6). An imported session may or may not be accepted by Google from a data-centre address. When it is not, the
  legitimate route is an official API or connector (P5b).
- **Downloads** (`browser.download`, A0, on by default) are enabled per task only when granted. The file waits in the browser
  container's tmpfs (quarantine) and is size-limited (`max_download_mb`, default 20). Executables (MZ, ELF, Mach-O, shebang) and
  archives (except `.docx`/`.xlsx`) are refused whatever their name. An allowed type (`download_types`) is written under
  `downloads/` with its sha256, and textual files get an untrusted preview.
- **Uploads** (`browser.upload`) are off unless the owner sets `browser.uploads_enabled`. Each upload is A2:
  - the approval binds the workspace path, the file's sha256 and size, the page and a real `<input type=file>`;
  - just before attaching, a changed file or page voids the approval;
  - the intent is recorded first, so a crash never leads to a blind resubmit.

## Evidence
`engine/browser_auth_test.go`, run against a fake browser host:
- **Login with profile and one-time code:** the Core types username, password and code. A scan of every table and column finds
  neither the password nor the code, with a positive control proving the scan works. No model prompt contains either. The saved
  session is encrypted and limited to the profile's domain.
- **Gates:** login without a granted profile is refused; a "password" field that is a text input is refused; an unknown profile
  is refused; a stray code is refused.
- **Downloads:** a CSV reaches `downloads/`; an executable named `.pdf`, a zip and a shell script are refused and never reach
  the workspace.
- **Uploads:** off by default; approval bound to the hash; a file changed after approval is not uploaded; an unchanged file is
  uploaded once.
- **Session import:** keeps only the profile's cookies, stored encrypted.

## Limits
- Not yet exercised on a real site with a real model. The free-model quota was exhausted and there is no credit.
- Headed or remote browser viewing, for the owner to log in interactively on the server, is not built. Session import is the
  human-mediated route.
- Malware scanning of downloads is not built; the type policy is the control.
