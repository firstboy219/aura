"""Worker entry point: serve Core requests until shutdown or EOF."""

from __future__ import annotations

import os
import resource
import sys
import traceback

from aura_worker import __version__, agent
from aura_worker.rpc import Channel, RpcError


def _health() -> dict:
    rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024
    return {"status": "ok", "version": __version__, "rss_bytes": rss}


def serve(ch: Channel) -> None:
    while True:
        msg = ch.read()
        if msg is None:
            return
        rid, method = msg.get("id"), msg.get("method")
        ch.meta = msg.get("meta") or {}
        agent.set_preamble(msg.get("params") or {})
        try:
            if method == "worker.health":
                result = _health()
            elif method == "worker.shutdown":
                ch.write({"id": rid, "result": {"accepted": True}})
                return
            elif method == "task.plan":
                result = agent.plan(ch, msg.get("params") or {})
            elif method == "task.step":
                result = agent.step(ch, msg.get("params") or {})
            elif method == "task.review":
                result = agent.review(ch, msg.get("params") or {})
            elif method == "task.replan":
                result = agent.replan(ch, msg.get("params") or {})
            elif method == "task.verify":
                result = agent.verify(ch, msg.get("params") or {})
            elif method == "task.reflect":
                result = agent.reflect(ch, msg.get("params") or {})
            elif method == "task.watch":
                result = agent.watch(ch, msg.get("params") or {})
            elif method == "task.diagnose":
                result = agent.diagnose(ch, msg.get("params") or {})
            elif method == "task.remedy_skill":
                result = agent.remedy_skill(ch, msg.get("params") or {})
            elif method == "task.build_tool":
                result = agent.build_tool(ch, msg.get("params") or {})
            elif method == "task.draft_brain_variant":
                result = agent.draft_brain_variant(ch, msg.get("params") or {})
            else:
                ch.write({"id": rid, "error": {"code": -32601, "message": f"unknown method {method}"}})
                continue
            ch.write({"id": rid, "result": result})
        except RpcError as e:
            ch.write({"id": rid, "error": {"code": e.code, "message": e.message, "data": {"reason_code": e.reason}}})
        except agent.StepFailure as e:
            ch.write({"id": rid, "error": {"code": -32602, "message": str(e), "data": {"reason_code": e.reason}}})
        except Exception as e:  # noqa: BLE001 - report, never crash silently
            print(traceback.format_exc(limit=5), file=sys.stderr)
            ch.write({"id": rid, "error": {"code": -32603, "message": f"worker error: {type(e).__name__}",
                                           "data": {"reason_code": "worker_exception"}}})


def main() -> None:
    os.umask(0o077)
    serve(Channel())


if __name__ == "__main__":
    main()
