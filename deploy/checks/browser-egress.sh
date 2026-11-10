#!/usr/bin/env bash
# P4 BRW-02 (no direct egress bypassing the proxy) + browser container isolation, probed INSIDE aura-browser.
# Usage: sudo deploy/checks/browser-egress.sh → Markdown report; exit 1 if a probe that must be denied is allowed.
set -uo pipefail
probe='
import json, os, socket, urllib.request
res = {"uid": os.getuid()}
def attempt(name, fn):
    try:
        r = fn(); res[name] = "ALLOWED" + (" " + str(r) if r else "")
    except Exception as e:
        res[name] = "denied (" + type(e).__name__ + ")"
attempt("TCP to the internet (1.1.1.1:443)", lambda: socket.create_connection(("1.1.1.1", 443), timeout=4) and None)
attempt("DNS for a public name (example.com)", lambda: socket.getaddrinfo("example.com", 443) and None)
attempt("TCP to the host (172.17.0.1:22)", lambda: socket.create_connection(("172.17.0.1", 22), timeout=4) and None)
attempt("cloud metadata (169.254.169.254:80)", lambda: socket.create_connection(("169.254.169.254", 80), timeout=4) and None)
def api_request():
    # TCP may complete in the kernel; what matters is that the Core serves nothing to this network.
    s = socket.create_connection(("aura-core", 7400), timeout=4)
    s.sendall(b"GET /api/v1/system/health HTTP/1.1\r\nHost: aura-core\r\n\r\n")
    s.settimeout(4)
    data = s.recv(200)
    if data:
        return data.split(b"\r\n")[0].decode()
    raise ConnectionResetError("closed by the Core before any response")
attempt("Core API request (GET /system/health)", api_request)
def proxy_noauth():
    s = socket.create_connection(("aura-core", 7480), timeout=4)
    s.sendall(b"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
    line = s.recv(200).split(b"\r\n")[0].decode()
    if " 200 " in line:
        return line
    raise PermissionError(line)
attempt("egress proxy without session credentials", proxy_noauth)
for p in ["/var/lib/aura/aura.db", "/run/aura-secrets/master.key", "/var/lib/aura/api_token"]:
    attempt("read " + p, lambda p=p: open(p, "rb").read(1) and None)
print(json.dumps(res))
'
out=$(docker exec aura-browser python3 -c "$probe") || { echo "probe failed to run"; exit 1; }
echo "# Browser egress check — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo
echo "Container: aura-browser · $(docker inspect -f 'networks={{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}read_only={{.HostConfig.ReadonlyRootfs}} cap_drop={{.HostConfig.CapDrop}} user={{.Config.User}}' aura-browser) · aura-browser-net internal=$(docker network inspect -f '{{.Internal}}' aura-browser-net)"
echo
echo "| Probe (inside the browser container) | Result |"
echo "|---|---|"
python3 -I -c 'import json,sys; d=json.loads(sys.argv[1]); [print(f"| {k} | {v} |") for k,v in d.items()]' "$out"
bad=$(python3 -I -c 'import json,sys; d=json.loads(sys.argv[1]); print(sum(1 for k,v in d.items() if str(v).startswith("ALLOWED") and k!="uid") + (0 if d["uid"]==10003 else 1))' "$out")
echo
if [ "$bad" = "0" ]; then echo "**Result: PASS** — no direct egress; the proxy refuses connections without a session; no AURA data visible."; else echo "**Result: FAIL** ($bad problems)"; exit 1; fi
