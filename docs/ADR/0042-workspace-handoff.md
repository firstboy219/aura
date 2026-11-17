# ADR-0042 — Workspace handoff and owner sign-in through the remote browser view

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
Owner can take over and return control. AURA pauses its actions on that session during takeover and resumes only after explicit
return-control and policy revalidation". It also gives Blueprint 8.8.1 rule 4 a human-mediated login path ("prefer user-mediated
login").

## Decision
- **Takeover** (`POST /tasks/{id}/browser/takeover`, Web App "Take over the browser"):
  - The task is paused, the session is marked owner-controlled, and every AURA browser tool on it is refused
    (`owner_has_control`).
  - The owner sees the page as a screenshot, refreshed every 3 s (`GET /browser/sessions/{key}/screen`), and acts on it
    (`POST /browser/sessions/{key}/input`): click at a point, allowlisted keys, typing, scroll, back, and opening a URL within
    the session's domains. The egress proxy enforces the domains for every request anyway.
- **Return control** (`POST /browser/sessions/{key}/return`):
  - Element refs and typed values are discarded, so AURA must inspect the page again before acting. This is the policy
    revalidation.
  - A logged-in profile's session is saved, and the task resumes only if the takeover paused it.
- **Owner sign-in for a login profile** (`POST /browser/profiles/{name}/login`, Web App "Browser → Sign in myself"):
  - It opens an owner-controlled session on the profile's domains plus optional extra sign-in domains (for example
    `accounts.google.com`), loading any saved session.
  - The owner signs in by hand, including MFA. `…/login/finish {save:true}` stores the cookies and storage, filtered to the
    profile's domains, encrypted in the vault, and closes the session.
  - AURA never sees or types these credentials.
- **Audit:** owner input is the owner acting, so it needs no approval. Each action is audited by kind, coordinates or URL.
  **Typed text is never recorded**, only its length.
- Stale sessions from a previous Core run are closed when browser automation is attached at startup.

## Evidence
`engine/browser_test.go`:
- **Takeover and return:** the task pauses; AURA's tools are refused; owner typing works and a scan of every table and column
  finds no trace of the typed text; navigation outside the domains is refused; the screen is returned; on return, control is
  released, refs are cleared and the task is no longer paused; return without a takeover is refused.
- **Owner profile login:** a start URL outside the profile is refused; owner input works; the session is saved encrypted and the
  view is closed.

## Limits
- The view is screenshot-based, not a live video stream, and is limited to the 1280×800 viewport. There is no drag-and-drop,
  file picking or clipboard.
- Google may still refuse sign-in from a headless browser in a data centre. That is the site's decision, and AURA does not work
  around it.
- Desktop (non-browser) handoff is not in scope.
