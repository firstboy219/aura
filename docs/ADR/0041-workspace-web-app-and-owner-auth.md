# ADR-0041 — Workspace Web App, owner authentication and remote exposure

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)
server-side authorization, approval UX, untrusted rendering, web security baseline, localization, efficiency) and the D.1 P5 web
security tests. Owner decisions: expose through **Cloudflare Tunnel** at `aura.cosger.online` (app) and `aurax.cosger.online`
(API); voice (OCA) is deferred.

## Decision
- **App:** a static single-page app (`internal/webui`, embedded in the Core binary) served at `/app/` on the web origin only. It
  uses the public API; there are no UI-only endpoints.
  - **Bahasa Indonesia and English:** all strings sit in one resource table.
  - **Layout:** responsive down to phone width.
  - **Screens:** tasks (create, list), task detail (state, reason, cost, question and answer, correction, pause/resume/cancel,
    files, the one-time code for browser logins, the activity log), approvals, notifications, L2 tools, self-description.
  - **Rendering:** every value is rendered with `textContent`, never as HTML. A strict CSP forbids inline and remote script and
    framing.
  - **Workspace files** are served as `text/plain` with `nosniff` and `CSP: sandbox`, so a downloaded HTML file can never run on
    the app's origin.
- **Owner authentication** (one owner, no multi-user model before its phase):
  - argon2id password (64 MiB, t=3) plus a **mandatory TOTP code** (RFC 6238). Each code is accepted once.
  - 5 failures lock the account for 15 minutes, and logins are rate-limited per client address.
  - The TOTP secret is encrypted in the vault.
- **Setup and recovery:** `aura web setup-link` creates a single-use link that is valid for 30 minutes and stored only as a
  hash. The token travels in the URL fragment, so it never reaches a server log. The owner sets the password, adds the
  authenticator and confirms with a first code. A reset signs every device out. `aura web sessions-revoke` signs everyone out.
- **Sessions:**
  - The cookie is `__Host-aura_session` (Secure, HttpOnly, SameSite=Strict, Path=/), stored server-side only as a sha256 hash.
    Lifetime is 12 h, and logout deletes the session.
  - The cookie is accepted **only on the web origin**. API hosts accept bearer tokens only.
  - Every state-changing request needs the session's CSRF token and, when present, a matching `Origin`.
- **Step-up:** approving an action or an L2 tool from the web needs a fresh authenticator code, bound to the exact action hash
  (13A.7 rule 4). Rejecting does not need one. The approver is recorded as `owner(web, step-up)`.
- **Approval screens** show the action and its exact parameters, target, side effect, risk tier, estimated cost (and the task's
  cost so far), task, step, why (the plan's step description), expiry and action hash.
- **Exposure:** an `aura-tunnel` container (`cloudflared` 2025.4.0, outbound only, uid 10001, read-only, 128 MiB) on `aura-net`.
  - It mounts only the tunnel token file and starts only with the `tunnel` compose profile.
  - No host port is opened, and no other product's proxy is used.
  - `CF-Connecting-IP` is trusted only from `aura-net`, and only for logging and rate limiting, never for authorization.

## Evidence
`app/web_test.go`:
- the setup link works once;
- cookie flags;
- GET works with the session, POST without CSRF or with a wrong CSRF token is refused, a cross-origin POST is refused, and the
  correct one is accepted;
- the cookie is refused on the API host, where bearer works, and the app is not served there;
- CSP and framing headers;
- a web approval without step-up is refused;
- logout invalidates the session server-side;
- every sampled endpoint refuses unauthenticated calls;
- a replayed code is refused, and lockout applies after failures even with the right password;
- login is not served outside the web host;
- a planted HTML file is served as sandboxed plain text.

## Limits
- Device-bound approval signatures (13A.7 rule 4, preferred for mobile) are not built; TOTP step-up stands in.
- WCAG 2.2 AA is targeted (labels, focus, keyboard, no meaning by colour alone) but not audited.
- No Server-Sent Events in the app: the task view polls every 6 s.
- Voice (OCA), workspace handoff of browser sessions, and the Admin foundation are not in this ADR.
