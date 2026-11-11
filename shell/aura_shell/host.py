"""AURA shell host (ADR-0065).

Runs in the aura-shell container: an internal network with no route out, so the only way to the internet is the
Core's egress proxy, with credentials the Core issues per command (public hosts only; private, loopback and
metadata addresses are refused by the proxy). The container holds no secret and no AURA data: each task gets its
own directory under /work, and files move in and out only when the Core copies them. The host serves the Core alone
(peer uid check) over a Unix socket, one JSON object per line.

Every command is decided and logged by the Core's watcher before it reaches this host; the host only runs it with
limits: wall-clock timeout, CPU, memory, processes, file size, output size. A timed-out command's whole process
group is killed.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import threading
import time
from typing import Any, ClassVar

WORK = os.environ.get("AURA_SHELL_WORK", "/work")
TASK_ID = re.compile(r"^[0-9A-Za-z_-]{1,64}$")
MAX_OUTPUT = 64 * 1024  # bytes kept of stdout and of stderr each
MAX_FILE = 20 * 1024 * 1024  # one file copied in or out
MAX_FILES = 10
MAX_COMMAND = 8000
MAX_TIMEOUT = 600
PATH = "/home/runner/.local/bin:/usr/local/bin:/usr/bin:/bin"


class HostError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


def task_dir(task_id: Any) -> str:
    t = str(task_id or "")
    if not TASK_ID.match(t):
        raise HostError("bad_params", "invalid task id")
    d = os.path.join(WORK, t)
    os.makedirs(d, mode=0o700, exist_ok=True)
    return d


def inside(base: str, rel: Any) -> str:
    """A path relative to the task directory; absolute paths, '..' and symlinks out are refused."""
    r = str(rel or "")
    if not r or r.startswith("/") or "\x00" in r or ".." in r.split("/") or len(r) > 300:
        raise HostError("bad_path", f"invalid path {r[:80]!r}")
    p = os.path.realpath(os.path.join(base, r))
    if p != base and not p.startswith(base + os.sep):
        raise HostError("bad_path", f"path leaves the task directory: {r[:80]!r}")
    return p


def limited(timeout: int, cmd: str) -> list[str]:
    """The command under prlimit (no preexec_fn: the host is multi-threaded)."""
    return ["prlimit", f"--cpu={timeout + 5}", f"--as={2 << 30}", "--nproc=256", f"--fsize={MAX_FILE * 10}", "--core=0",
            "--", "/bin/bash", "-c", cmd]


class Host:
    def __init__(self) -> None:
        self.started = time.time()
        self.running: dict[str, subprocess.Popen] = {}
        self.lock = threading.Lock()

    def exec(self, p: dict) -> dict:
        cmd = str(p.get("command") or "")
        if not cmd.strip() or len(cmd) > MAX_COMMAND:
            raise HostError("bad_params", f"command must be 1..{MAX_COMMAND} characters")
        timeout = max(1, min(int(p.get("timeout_s") or 120), MAX_TIMEOUT))
        base = task_dir(p.get("task_id"))
        files_in = p.get("files_in") or []
        files_out = p.get("files_out") or []
        if len(files_in) > MAX_FILES or len(files_out) > MAX_FILES:
            raise HostError("bad_params", f"at most {MAX_FILES} files each way")
        for f in files_in:
            data = base64.b64decode(str(f.get("content_base64", "")))
            if len(data) > MAX_FILE:
                raise HostError("too_large", "input file over 20 MiB")
            dest = inside(base, f.get("path"))
            os.makedirs(os.path.dirname(dest), mode=0o700, exist_ok=True)
            with open(dest, "wb") as fh:
                fh.write(data)
        env = {"PATH": PATH, "HOME": base, "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "TERM": "dumb", "TMPDIR": "/tmp",
               "PYTHONUSERBASE": os.path.join(base, ".local"), "PIP_DISABLE_PIP_VERSION_CHECK": "1"}
        proxy = str(p.get("proxy") or "")
        if proxy:
            for k in ("HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY"):
                env[k] = proxy
        run_id = str(p.get("run_id") or "")
        started = time.monotonic()
        proc = subprocess.Popen(limited(timeout, cmd), cwd=base, env=env, stdin=subprocess.DEVNULL,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        with self.lock:
            self.running[run_id] = proc
        timed_out = False
        try:
            out, err = proc.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            self._kill(proc)
            out, err = proc.communicate()
        finally:
            with self.lock:
                self.running.pop(run_id, None)
        files = []
        for rel in files_out:
            path = inside(base, rel)
            if not os.path.isfile(path) or os.path.islink(path):
                files.append({"path": rel, "missing": True})
                continue
            size = os.path.getsize(path)
            if size > MAX_FILE:
                files.append({"path": rel, "too_large": True, "bytes": size})
                continue
            with open(path, "rb") as fh:
                files.append({"path": rel, "bytes": size, "content_base64": base64.b64encode(fh.read()).decode()})
        return {
            "exit_code": proc.returncode, "timed_out": timed_out, "duration_ms": int((time.monotonic() - started) * 1000),
            "stdout": out[:MAX_OUTPUT].decode("utf-8", "replace"), "stderr": err[:MAX_OUTPUT].decode("utf-8", "replace"),
            "stdout_bytes": len(out), "stderr_bytes": len(err), "files": files,
        }

    @staticmethod
    def _kill(proc: subprocess.Popen) -> None:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except OSError:
            pass

    def kill_all(self, _: dict) -> dict:
        with self.lock:
            procs = list(self.running.values())
        for proc in procs:
            self._kill(proc)
        return {"killed": len(procs)}

    def cleanup(self, p: dict) -> dict:
        t = str(p.get("task_id") or "")
        if not TASK_ID.match(t):
            raise HostError("bad_params", "invalid task id")
        d = os.path.join(WORK, t)
        if os.path.isdir(d):
            shutil.rmtree(d, ignore_errors=True)
        return {"removed": t}

    def health(self, _: dict) -> dict:
        with self.lock:
            n = len(self.running)
        return {"status": "ok", "running": n, "uptime_s": int(time.time() - self.started)}

    METHODS: ClassVar[dict] = {"shell.exec": exec, "shell.kill_all": kill_all, "shell.cleanup": cleanup, "host.health": health}

    def handle(self, msg: dict) -> dict:
        fn = self.METHODS.get(str(msg.get("method")))
        if fn is None:
            return {"id": msg.get("id"), "error": {"code": "unknown_method", "message": "method not available"}}
        try:
            return {"id": msg.get("id"), "result": fn(self, msg.get("params") or {})}
        except HostError as e:
            return {"id": msg.get("id"), "error": {"code": e.code, "message": str(e)}}
        except (KeyError, ValueError, TypeError, OSError) as e:
            return {"id": msg.get("id"), "error": {"code": "bad_params", "message": str(e)[:200]}}


def peer_uid(conn: socket.socket) -> int:
    creds = conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i"))
    return struct.unpack("3i", creds)[1]


def serve_conn(host: Host, conn: socket.socket) -> None:
    f = conn.makefile("rwb", buffering=0)
    try:
        for line in f:
            if len(line) > 64 << 20:
                break
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            f.write((json.dumps(host.handle(msg)) + "\n").encode())
    except OSError:
        pass
    finally:
        conn.close()


def serve(path: str, allow_uid: int) -> None:
    host = Host()
    if os.path.exists(path):
        os.unlink(path)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(path)
    os.chmod(path, 0o666)  # the socket volume is shared only with the Core; the uid check authorizes
    srv.listen(8)
    print(json.dumps({"msg": "shell host listening", "socket": path}), flush=True)
    while True:
        conn, _ = srv.accept()
        if allow_uid >= 0 and peer_uid(conn) != allow_uid:
            conn.close()
            continue
        # One connection per command at a time: commands of different tasks may run side by side.
        threading.Thread(target=serve_conn, args=(host, conn), daemon=True).start()


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--socket", default="/run/aura-shell/shell.sock")
    ap.add_argument("--allow-uid", type=int, default=10001)
    a = ap.parse_args()
    try:
        serve(a.socket, a.allow_uid)
    except KeyboardInterrupt:
        sys.exit(0)


if __name__ == "__main__":
    main()
