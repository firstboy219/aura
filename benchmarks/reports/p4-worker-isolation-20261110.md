# Worker isolation check — 2026-11-10T01:04:54Z

Container: aura-worker · network_mode=none read_only=true cap_drop=[ALL] security_opt=[no-new-privileges:true] mounts=/run/aura-worker 

| Probe (as the worker identity) | Result |
|---|---|
| uid | 10002 |
| gid | 10002 |
| read AURA database /var/lib/aura/aura.db | denied (FileNotFoundError) |
| read API token /var/lib/aura/api_token | denied (FileNotFoundError) |
| read provider key /run/aura-secrets/openrouter_key | denied (FileNotFoundError) |
| list task workspaces /var/lib/aura/workspace | denied (FileNotFoundError) |
| TCP to the internet (1.1.1.1:443) | denied (OSError) |
| TCP to the Core API (aura-core:7400) | denied (gaierror) |
| TCP to the host (172.17.0.1:22) | denied (OSError) |
| DNS lookup (example.com) | denied (gaierror) |
| UDP to the internet (8.8.8.8:53) | denied (OSError) |
| write outside scratch (/usr/local/x) | denied (OSError) |
| write the root filesystem (/etc/x) | denied (OSError) |
| network interfaces | ['lo'] |

**Result: PASS** — every probe denied; only the loopback interface exists; uid 10002.
