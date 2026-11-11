#!/usr/bin/env bash
# Checks the aura-shell sandbox boundary on the running stack (ADR-0065). Run: sudo deploy/checks/shell-isolation.sh
set -u
fail=0
check() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1"; fail=1; fi; }
x() { docker exec aura-shell bash -c "$1" >/dev/null 2>&1; }
check "runs as an unprivileged user"            "[ \"\$(docker exec aura-shell id -u)\" = 10004 ]"
check "no AURA data mounted"                    "! x 'test -e /var/lib/aura'"
check "no secrets mounted"                      "! x 'test -e /run/aura-secrets || test -e /opt/aura'"
check "no Docker socket"                        "! x 'test -e /var/run/docker.sock'"
check "root file system is read-only"           "! x 'touch /usr/aura-test'"
check "no direct route to the internet"         "! x 'curl -s -m 5 --noproxy \"*\" https://example.com'"
check "Core API refuses the shell network"      "! x 'curl -sf -m 5 --noproxy \"*\" http://aura-core:7400/api/v1/system/health'"
check "proxy refuses without credentials"       "! x 'curl -sf -m 8 -x http://aura-core:7480 https://example.com'"
exit $fail
