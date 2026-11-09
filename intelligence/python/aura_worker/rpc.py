"""Newline-delimited JSON-RPC 2.0 over stdio (ADR-0003).

The worker is single-threaded: while it handles one Core request it may send its
own requests to the Core (llm.complete, tool.invoke, ...) and block for each
response. The Core never interleaves another request meanwhile.
"""

from __future__ import annotations

import itertools
import json
import sys
from typing import IO, Any

MAX_MESSAGE = 4 << 20


class RpcError(Exception):
    def __init__(self, code: int, message: str, reason: str = ""):
        super().__init__(message)
        self.code = code
        self.message = message
        self.reason = reason


class Channel:
    def __init__(self, rfile: IO[str] | None = None, wfile: IO[str] | None = None):
        self.r = rfile or sys.stdin
        self.w = wfile or sys.stdout
        self._ids = itertools.count(1)
        self.meta: dict[str, Any] = {}

    def read(self) -> dict | None:
        line = self.r.readline(MAX_MESSAGE + 1)
        if not line:
            return None
        if len(line) > MAX_MESSAGE:
            raise RpcError(-32600, "message too large", "too_large")
        return json.loads(line)

    def write(self, msg: dict) -> None:
        msg["jsonrpc"] = "2.0"
        data = json.dumps(msg, ensure_ascii=False, separators=(",", ":"))
        if len(data) >= MAX_MESSAGE:
            raise RpcError(-32600, "message too large", "too_large")
        self.w.write(data + "\n")
        self.w.flush()

    def call(self, method: str, params: dict) -> Any:
        """Send a worker→Core request with the active call's meta and wait for its response."""
        rid = f"w-{next(self._ids)}"
        meta = {k: self.meta[k] for k in ("task_id", "step_id", "trace_id", "correlation_id",
                                          "capability_token", "deadline_unix_ms") if k in self.meta}
        self.write({"id": rid, "method": method, "params": params, "meta": meta})
        while True:
            msg = self.read()
            if msg is None:
                raise RpcError(-32603, "core closed the channel", "eof")
            if msg.get("id") != rid or "method" in msg:
                # The Core does not interleave requests; anything else is a protocol error.
                raise RpcError(-32600, "unexpected message while waiting for response", "protocol")
            if msg.get("error"):
                e = msg["error"]
                raise RpcError(e.get("code", -32603), e.get("message", ""), (e.get("data") or {}).get("reason_code", ""))
            return msg.get("result")
