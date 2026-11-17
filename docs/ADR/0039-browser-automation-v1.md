# ADR-0039 — Browser Automation v1

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Decision
- **Runtime:** a separate container, `aura-browser`, running Playwright 1.49.1 (Apache-2.0) with Chromium 131 (BSD) on Debian
  bookworm, headless. Its security settings: uid 10003; read-only root; 512 MiB tmpfs; `cap_drop: ALL`; `no-new-privileges`;
  1 GiB memory and 512 pids; no data, workspace or secret mounts. Chromium runs without its own sandbox. The container, the
  unprivileged uid and the network boundary are the isolation (Blueprint 8.8.1 rule 7).
- **Network:** `aura-browser-net` is an internal Docker network with no gateway, on the pinned subnet 172.31.250.0/24.
  - The browser can reach only containers on that network.
  - The Core joins it and runs the **egress proxy** on `:7480`, which is not published.
  - The Core API closes every connection from that subnet before reading a request (`server.deny_cidrs`). TCP may still
    complete in the kernel, because no firewall rule is possible without `NET_ADMIN`.
- **Egress proxy** (`internal/egress`): every page, subresource, frame and WebSocket is a CONNECT or absolute-URI request. Each is
  checked against:
  - the session credentials (one random token per task session);
  - the task's domain allowlist;
  - ports 80 and 443 only;
  - the SSRF guard shared with `web.fetch`: DNS resolved once, every address checked, the checked IP dialled.

  Refusals are recorded in the task trace (`EGRESS_DENIED`). Ending a session revokes its credentials.
- **Browser host** (`browser/aura_browser`): a narrow command set over a Unix socket, served to the Core's uid only:
  `session.open/close/close_all` and `page.goto/inspect/describe/click/type/select/scroll/wait/back/screenshot`.
  - There is **no** command for model-chosen JavaScript, devtools, cookies, downloads or uploads. Downloads are off and
    service workers are blocked.
  - The only JavaScript run is the host's fixed element description.
  - Element refs come from the host's last inspect and are invalidated by navigation.
- **Core tools** (`browser.*`, manifest-governed):
  - `open`, `inspect`, `screenshot`, `scroll`, `wait` and `back` are A0, within the task's domains. `open` needs a planned URL or
    a URL with provenance.
  - `type` and `select` are A0, because nothing is sent. The value must equal the plan's value and is never taken from page
    content. Typed values are not traced; only the character count is.
  - `click` is **A2**, side effect `write_external`. The approval hash covers the page URL, the element (tag, name, href) and
    every value typed on that page. Before the approved click, the Core re-describes the element. If the page, the element or
    the typed values changed, the click is refused (BRW-05).
  - The intent is durable before the click, so a crash leaves an unknown outcome for the owner, never a blind resubmit (BRW-07,
    existing ADR-0013/0020 path).
- **One session per task:** its own BrowserContext, so cookies and storage are isolated (BRW-04). It is closed when the task
  finishes, by the kill switch, and at Core start.
- **Blocked sites (rule 6a, BRW-08):**
  - A bot wall, CAPTCHA or access-denied page stops the task with `SITE_BLOCKED`. There is no replan and no retry. The legitimate
    alternatives are stated: an official API or connector, or a human handoff.
  - An error page, including the proxy's refusal, is a tool failure and never treated as content.
- `aura browser check <url>` is an owner diagnostic that runs the real stack without a model.

## Evidence
- `internal/egress` tests: session credentials; allowlist; ports; an allowlisted name resolving to a private address or to
  metadata; the metadata IP literal; ended sessions; CONNECT tunnels carry traffic; plain HTTP.
- `engine/browser_test.go` with a fake host:
  - governed browsing end to end, with the A2 approval showing page, element and value;
  - a click refused after the element changed;
  - `SITE_BLOCKED` with no replan;
  - binding of refs and values to the Core's view and the plan;
  - an error page is a failure;
  - the kill switch closes sessions.
- `internal/browser` test: reconnect after a host restart, with no double execution.
- Production (`benchmarks/reports/p4-browser-20261110.md`): `deploy/checks/browser-egress.sh` PASS (no direct internet, DNS, host
  or metadata access; the API serves nothing to the browser network; the proxy refuses requests without a session; no AURA data
  visible); live checks through the proxy; PERF-14 measurements.

## Limits
- RS-8 with a real model is not yet run. The free-model quota was exhausted and there is no credit for paid routes.
- The browser image (1.99 GB) is not in the signed release manifest yet, and not yet slimmed.
- Login and authenticated sessions (human-mediated or delegated) are not built in v1. Downloads and uploads are off.
- Subresources from domains outside the allowlist are refused, so some sites render incompletely. Widening the allowlist is
  the owner's choice.
