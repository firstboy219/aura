#!/usr/bin/env bash
# Create an AURA member (a client of the API, e.g. JARVIS as front end — ADR-0066) and write its token to a file, never to the screen.
#   sudo deploy/member-token.sh <name> <viewer|operator|approver> <token-file> [group-that-may-read]
# The owner runs this: creating a credential is an owner decision. The token is shown nowhere and the file is 0640.
set -euo pipefail
NAME=${1:?usage: member-token.sh <name> <role> <token-file> [group]}
ROLE=${2:?role: viewer|operator|approver}
OUT=${3:?token file}
GROUP=${4:-root}
install -d -m 0750 -o root -g "$GROUP" "$(dirname "$OUT")"
umask 027
docker exec -i aura-core python3 - "$NAME" "$ROLE" <<'PY' > "$OUT"
import json, sys, urllib.request, urllib.error
tok = open('/var/lib/aura/api_token').read().strip()
req = urllib.request.Request('http://127.0.0.1:7400/api/v1/admin/members', method='POST',
    data=json.dumps({"name": sys.argv[1], "role": sys.argv[2], "department": ""}).encode(),
    headers={'Authorization': 'Bearer ' + tok, 'Content-Type': 'application/json'})
try:
    out = json.loads(urllib.request.urlopen(req, timeout=20).read())
except urllib.error.HTTPError as e:
    sys.stderr.write("AURA refused: HTTP %d %s\n" % (e.code, e.read()[:200].decode(errors="replace"))); sys.exit(1)
sys.stdout.write(out["token"])
PY
chown root:"$GROUP" "$OUT" && chmod 0640 "$OUT"
echo "member $NAME ($ROLE) created; token written to $OUT ($(wc -c < "$OUT") bytes, not shown)"
