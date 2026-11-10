#!/usr/bin/env bash
# P4 acceptance (Blueprint D.1 P4): the worker runs isolated under a separate unprivileged identity with default-deny network;
# direct DB, network and secret access fail. Runs probes INSIDE the aura-worker container, with the worker's own identity.
# Usage: sudo deploy/checks/worker-isolation.sh  → prints a Markdown report; exit 1 if any probe is not denied.
set -uo pipefail
probe='
import json, os, socket, sys
res = {}
def attempt(name, fn):
    try:
        fn(); res[name] = "ALLOWED"
    except Exception as e:
        res[name] = "denied (" + type(e).__name__ + ")"
res["uid"] = os.getuid(); res["gid"] = os.getgid()
attempt("read AURA database /var/lib/aura/aura.db", lambda: open("/var/lib/aura/aura.db", "rb").read(1))
attempt("read API token /var/lib/aura/api_token", lambda: open("/var/lib/aura/api_token").read())
attempt("read provider key /run/aura-secrets/openrouter_key", lambda: open("/run/aura-secrets/openrouter_key").read())
attempt("list task workspaces /var/lib/aura/workspace", lambda: os.listdir("/var/lib/aura/workspace"))
attempt("TCP to the internet (1.1.1.1:443)", lambda: socket.create_connection(("1.1.1.1", 443), timeout=3))
attempt("TCP to the Core API (aura-core:7400)", lambda: socket.create_connection(("aura-core", 7400), timeout=3))
attempt("TCP to the host (172.17.0.1:22)", lambda: socket.create_connection(("172.17.0.1", 22), timeout=3))
attempt("DNS lookup (example.com)", lambda: socket.getaddrinfo("example.com", 443))
def udp():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(2); s.sendto(b"x", ("8.8.8.8", 53)); s.recvfrom(10)
attempt("UDP to the internet (8.8.8.8:53)", udp)
attempt("write outside scratch (/usr/local/x)", lambda: open("/usr/local/x", "w"))
attempt("write the root filesystem (/etc/x)", lambda: open("/etc/x", "w"))
ifaces = [n for _, n in socket.if_nameindex()]
res["network interfaces"] = ifaces
print(json.dumps(res))
'
out=$(docker exec -u 10002:10002 aura-worker python3 -I -c "$probe") || { echo "probe failed to run"; exit 1; }
echo "# Worker isolation check — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo
echo "Container: aura-worker · $(docker inspect -f 'network_mode={{.HostConfig.NetworkMode}} read_only={{.HostConfig.ReadonlyRootfs}} cap_drop={{.HostConfig.CapDrop}} security_opt={{.HostConfig.SecurityOpt}} mounts={{range .Mounts}}{{.Destination}} {{end}}' aura-worker)"
echo
echo "| Probe (as the worker identity) | Result |"
echo "|---|---|"
python3 -I -c 'import json,sys; d=json.loads(sys.argv[1]); [print(f"| {k} | {v} |") for k,v in d.items()]' "$out"
bad=$(python3 -I -c 'import json,sys; d=json.loads(sys.argv[1]); print(sum(1 for v in d.values() if v=="ALLOWED") + (0 if d["network interfaces"]==["lo"] else 1) + (0 if d["uid"]==10002 else 1))' "$out")
echo
if [ "$bad" = "0" ]; then echo "**Result: PASS** — every probe denied; only the loopback interface exists; uid 10002."; else echo "**Result: FAIL** ($bad problems)"; exit 1; fi
