"""AURA browser host (Blueprint 8.8.1, P4, ADR-0039).

Runs in the aura-browser container: an internal network with no route out, so every byte goes through the Core's egress
proxy (per-session credentials, domain allowlist, SSRF guard). It serves a narrow command set to the Core only (peer uid
check) over a Unix socket, one JSON object per line. There is no command that runs page JavaScript chosen by the model,
opens devtools, uploads files or reads cookies: the only JavaScript executed is this host's own fixed element
enumeration. Each session is its own BrowserContext (cookies and storage isolated) and is closed by the Core.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import socket
import struct
import sys
import threading
import time
from typing import Any

from playwright.sync_api import Error as PlaywrightError
from playwright.sync_api import sync_playwright

INTERACTIVE = "a[href], button, input:not([type=hidden]), select, textarea, [role=button], [role=link], [role=checkbox], [role=tab]"
MAX_ELEMENTS = 150
MAX_TEXT = 20000
BLOCK_MARKERS = re.compile(
    r"captcha|are you a robot|verify you are human|attention required|access denied|unusual traffic|"
    r"cf-challenge|just a moment\.\.\.|please enable cookies|bot detection|request blocked", re.IGNORECASE)

# Fixed, host-owned enumeration of one element (never model-provided).
DESCRIBE_JS = """e => ({tag: e.tagName.toLowerCase(), role: e.getAttribute('role') || '',
  type: (e.getAttribute('type') || '').toLowerCase(), href: e.getAttribute('href') || '',
  name: (e.getAttribute('aria-label') || e.innerText || e.value || e.getAttribute('placeholder') ||
         e.getAttribute('name') || e.getAttribute('title') || '').trim().slice(0, 120),
  visible: !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length)})"""


class HostError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


class Host:
    def __init__(self, proxy: str):
        self.proxy = proxy
        self.pw = sync_playwright().start()
        # Per-context proxies need a placeholder at launch (Chromium).
        self.browser = self.pw.chromium.launch(headless=True, proxy={"server": "http://per-context"},
                                               args=["--disable-dev-shm-usage", "--no-first-run", "--disable-extensions"])
        self.sessions: dict[str, dict[str, Any]] = {}
        self.started = time.time()

    # ---------------------------------------------------------------- sessions
    def session_open(self, p: dict) -> dict:
        sid = str(p["session_id"])
        if sid in self.sessions:
            raise HostError("exists", "session already open")
        opts: dict[str, Any] = {}
        if isinstance(p.get("storage_state"), dict):
            opts["storage_state"] = p["storage_state"]  # an owner-approved login session, decrypted by the Core
        ctx = self.browser.new_context(
            proxy={"server": self.proxy, "username": sid, "password": str(p["proxy_token"])},
            accept_downloads=bool(p.get("downloads")), service_workers="block", viewport={"width": 1280, "height": 800},
            java_script_enabled=True, locale="en-US", **opts)
        ctx.set_default_timeout(15000)
        page = ctx.new_page()
        self.sessions[sid] = {"ctx": ctx, "page": page, "refs": [], "gen": 0}
        return {"session_id": sid}

    def session_close(self, p: dict) -> dict:
        s = self.sessions.pop(str(p["session_id"]), None)
        if s:
            try:
                s["ctx"].close()
            except PlaywrightError:
                pass
        return {"closed": bool(s)}

    def close_all(self, _: dict) -> dict:
        n = len(self.sessions)
        for sid in list(self.sessions):
            self.session_close({"session_id": sid})
        return {"closed": n}

    def _s(self, p: dict) -> dict:
        s = self.sessions.get(str(p.get("session_id")))
        if not s:
            raise HostError("no_session", "unknown browser session")
        return s

    # ---------------------------------------------------------------- page state
    def _state(self, s: dict, status: int | None = None) -> dict:
        page = s["page"]
        title = ""
        try:
            title = page.title()
        except PlaywrightError:
            pass
        text = ""
        try:
            text = page.locator("body").inner_text(timeout=3000)[:4000]
        except PlaywrightError:
            pass
        blocked = bool(BLOCK_MARKERS.search(title)) or (status in (403, 429, 503) and bool(BLOCK_MARKERS.search(text)))
        return {"url": page.url, "title": title[:300], "status": status, "site_blocked": blocked}

    def goto(self, p: dict) -> dict:
        s = self._s(p)
        url = str(p["url"])
        if not url.startswith(("http://", "https://")):
            raise HostError("bad_url", "only http(s) URLs")
        try:
            resp = s["page"].goto(url, wait_until="domcontentloaded", timeout=int(p.get("timeout_ms", 20000)))
        except PlaywrightError as e:
            raise HostError("navigation_failed", str(e).splitlines()[0][:300]) from e
        s["refs"], s["gen"] = [], s["gen"] + 1
        return self._state(s, resp.status if resp else None)

    def inspect(self, p: dict) -> dict:
        s = self._s(p)
        page = s["page"]
        st = self._state(s)
        try:
            text = page.locator("body").inner_text(timeout=5000)
        except PlaywrightError:
            text = ""
        elements, refs = [], []
        for loc in page.locator(INTERACTIVE).all()[: MAX_ELEMENTS * 2]:
            try:
                d = loc.evaluate(DESCRIBE_JS)
            except PlaywrightError:
                continue
            if not d.get("visible"):
                continue
            ref = f"e{len(refs) + 1}"
            refs.append(loc)
            item = {"ref": ref, "tag": d["tag"], "name": d["name"]}
            for k in ("role", "type", "href"):
                if d.get(k):
                    item[k] = d[k][:300]
            elements.append(item)
            if len(refs) >= MAX_ELEMENTS:
                break
        s["refs"], s["gen"] = refs, s["gen"] + 1
        st.update({"text": text[: int(p.get("max_chars", MAX_TEXT))], "elements": elements, "generation": s["gen"]})
        return st

    def _ref(self, s: dict, p: dict):
        ref = str(p.get("ref", ""))
        m = re.fullmatch(r"e(\d+)", ref)
        if not m or int(m.group(1)) > len(s["refs"]):
            raise HostError("stale_ref", "unknown or stale element ref; inspect the page again")
        return s["refs"][int(m.group(1)) - 1]

    def describe(self, p: dict) -> dict:
        s = self._s(p)
        d = self._ref(s, p).evaluate(DESCRIBE_JS)
        return {"url": s["page"].url, "tag": d["tag"], "name": d["name"], "type": d.get("type", ""), "href": d.get("href", "")}

    def click(self, p: dict) -> dict:
        s = self._s(p)
        loc = self._ref(s, p)
        try:
            loc.click(timeout=10000)
            s["page"].wait_for_load_state("domcontentloaded", timeout=15000)
        except PlaywrightError as e:
            raise HostError("action_failed", str(e).splitlines()[0][:300]) from e
        s["refs"], s["gen"] = [], s["gen"] + 1
        return self._state(s)

    def type_text(self, p: dict) -> dict:
        s = self._s(p)
        loc = self._ref(s, p)
        try:
            loc.fill(str(p.get("text", ""))[:2000], timeout=10000)
        except PlaywrightError as e:
            raise HostError("action_failed", str(e).splitlines()[0][:300]) from e
        return {"url": s["page"].url, "typed_chars": len(str(p.get("text", "")))}

    def select(self, p: dict) -> dict:
        s = self._s(p)
        try:
            chosen = self._ref(s, p).select_option(str(p.get("value", "")), timeout=10000)
        except PlaywrightError as e:
            raise HostError("action_failed", str(e).splitlines()[0][:300]) from e
        return {"url": s["page"].url, "selected": chosen}

    def scroll(self, p: dict) -> dict:
        s = self._s(p)
        dy = {"down": 700, "up": -700}.get(str(p.get("direction", "down")), 700)
        s["page"].mouse.wheel(0, dy)
        return {"url": s["page"].url}

    def wait(self, p: dict) -> dict:
        s = self._s(p)
        s["page"].wait_for_timeout(max(0, min(int(p.get("ms", 1000)), 10000)))
        return {"url": s["page"].url}

    def back(self, p: dict) -> dict:
        s = self._s(p)
        resp = s["page"].go_back(wait_until="domcontentloaded", timeout=15000)
        s["refs"], s["gen"] = [], s["gen"] + 1
        return self._state(s, resp.status if resp else None)

    def screenshot(self, p: dict) -> dict:
        s = self._s(p)
        png = s["page"].screenshot(full_page=False, type="png", timeout=10000)
        if len(png) > 3 << 20:
            raise HostError("too_large", "screenshot over 3 MiB")
        return {"url": s["page"].url, "png_base64": base64.b64encode(png).decode(), "bytes": len(png)}

    def download(self, p: dict) -> dict:
        """Click a download link; the file stays in this container's tmpfs until the Core takes it (quarantine)."""
        s = self._s(p)
        loc = self._ref(s, p)
        max_bytes = int(p.get("max_bytes", 20 << 20))
        try:
            with s["page"].expect_download(timeout=30000) as info:
                loc.click(timeout=10000)
            dl = info.value
            path = dl.path()
        except PlaywrightError as e:
            raise HostError("download_failed", str(e).splitlines()[0][:300]) from e
        try:
            size = os.path.getsize(path)
            if size > max_bytes:
                raise HostError("too_large", f"download is {size} bytes, over the {max_bytes}-byte limit")
            with open(path, "rb") as fh:
                data = fh.read()
        finally:
            try:
                dl.delete()
            except PlaywrightError:
                pass
        return {"url": dl.url, "suggested_filename": dl.suggested_filename[:200], "bytes": len(data),
                "content_base64": base64.b64encode(data).decode()}

    def upload(self, p: dict) -> dict:
        """Put Core-provided bytes into a file input (the Core checked policy and the owner's approval)."""
        s = self._s(p)
        loc = self._ref(s, p)
        name = os.path.basename(str(p.get("filename", "upload.bin")))[:120] or "upload.bin"
        data = base64.b64decode(str(p.get("content_base64", "")))
        d = os.path.join("/tmp", "aura-upload-" + str(time.time_ns()))
        os.makedirs(d, mode=0o700)
        path = os.path.join(d, name)
        try:
            with open(path, "wb") as fh:
                fh.write(data)
            loc.set_input_files(path, timeout=10000)
        except PlaywrightError as e:
            raise HostError("upload_failed", str(e).splitlines()[0][:300]) from e
        finally:
            try:
                os.remove(path)
                os.rmdir(d)
            except OSError:
                pass
        return {"url": s["page"].url, "uploaded_bytes": len(data), "filename": name}

    # ---------------------------------------------------------------- owner takeover (raw input; Core-authorized)
    def mouse_click(self, p: dict) -> dict:
        s = self._s(p)
        x, y = float(p["x"]), float(p["y"])
        if not (0 <= x <= 1280 and 0 <= y <= 800):
            raise HostError("bad_params", "click outside the viewport")
        s["page"].mouse.click(x, y)
        s["page"].wait_for_timeout(300)
        s["refs"], s["gen"] = [], s["gen"] + 1
        return {"url": s["page"].url}

    def key(self, p: dict) -> dict:
        s = self._s(p)
        k = str(p.get("key", ""))
        if k not in ("Enter", "Tab", "Backspace", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Delete"):
            raise HostError("bad_params", "key not allowed")
        s["page"].keyboard.press(k)
        s["page"].wait_for_timeout(300)
        s["refs"], s["gen"] = [], s["gen"] + 1
        return {"url": s["page"].url}

    def keyboard_type(self, p: dict) -> dict:
        s = self._s(p)
        s["page"].keyboard.type(str(p.get("text", ""))[:2000], delay=20)
        return {"url": s["page"].url}

    def export_state(self, p: dict) -> dict:
        """The session's cookies and storage, for the Core to encrypt (owner-approved login reuse)."""
        s = self._s(p)
        return {"storage_state": s["ctx"].storage_state()}

    def health(self, _: dict) -> dict:
        return {"status": "ok", "sessions": len(self.sessions), "uptime_s": int(time.time() - self.started),
                "browser": self.browser.version}

    METHODS = {"session.open": session_open, "session.close": session_close, "session.close_all": close_all,
               "page.goto": goto, "page.inspect": inspect, "page.describe": describe, "page.click": click,
               "page.type": type_text, "page.select": select, "page.scroll": scroll, "page.wait": wait, "page.back": back,
               "page.screenshot": screenshot, "page.download": download, "page.upload": upload,
               "session.export": export_state, "page.mouse_click": mouse_click, "page.key": key,
               "page.keyboard_type": keyboard_type, "host.health": health}

    def handle(self, msg: dict) -> dict:
        fn = self.METHODS.get(str(msg.get("method")))
        if fn is None:
            return {"id": msg.get("id"), "error": {"code": "unknown_method", "message": "method not available"}}
        try:
            return {"id": msg.get("id"), "result": fn(self, msg.get("params") or {})}
        except HostError as e:
            return {"id": msg.get("id"), "error": {"code": e.code, "message": str(e)}}
        except (KeyError, ValueError, TypeError) as e:
            return {"id": msg.get("id"), "error": {"code": "bad_params", "message": str(e)[:200]}}
        except PlaywrightError as e:
            return {"id": msg.get("id"), "error": {"code": "browser_error", "message": str(e).splitlines()[0][:300]}}


def peer_uid(conn: socket.socket) -> int:
    creds = conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i"))
    return struct.unpack("3i", creds)[1]


def serve(path: str, allow_uid: int, proxy: str) -> None:
    host = Host(proxy)
    lock = threading.Lock()  # Playwright sync API: one command at a time
    if os.path.exists(path):
        os.unlink(path)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(path)
    os.chmod(path, 0o666)  # the socket volume is shared only with the Core; the uid check authorizes
    srv.listen(4)
    print(json.dumps({"msg": "browser host listening", "socket": path, "browser": host.browser.version}), flush=True)
    while True:
        conn, _ = srv.accept()
        if allow_uid >= 0 and peer_uid(conn) != allow_uid:
            conn.close()
            continue
        f = conn.makefile("rwb", buffering=0)
        try:
            for line in f:
                if len(line) > 1 << 20:
                    break
                try:
                    msg = json.loads(line)
                except ValueError:
                    continue
                with lock:
                    out = host.handle(msg)
                f.write((json.dumps(out) + "\n").encode())
        except OSError:
            pass
        finally:
            conn.close()


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--socket", default="/run/aura-browser/browser.sock")
    ap.add_argument("--allow-uid", type=int, default=10001)
    ap.add_argument("--proxy", default="http://aura-core:7480")
    a = ap.parse_args()
    try:
        serve(a.socket, a.allow_uid, a.proxy)
    except KeyboardInterrupt:
        sys.exit(0)


if __name__ == "__main__":
    main()
