# SECURITY_MODEL.md — AURA Phase 1 threat model and controls

Status: written in P0 from Master Blueprint §3, §5.1, §7, §8.1–8.5, §8.8, §10, §11, §13 for **owner review**
(the Blueprint names this file as the precedence document for security boundaries; it was not supplied, A-02).
Evidence level: **design-reviewed**. Security does not rely on source secrecy (§3.3).

## 1. Assets
Provider key (shared with Jarvis, A-04); API bearer token; task data, workspace files, event/audit log; budget; host
resources shared with Jarvis.

## 2. Trust zones (Blueprint §7)
| Zone | Trust | P1 enforcement |
|---|---|---|
| Go Core | TCB | Only DB writer, only secret holder, final authority |
| Owner via CLI/REST | Trusted for granted actions | Bearer token, loopback bind, rate limit, audit |
| Python worker | Semi-trusted | No keys/DB/network by architecture; every request checked against grant (ADR-0006) |
| Model output, fetched content, workspace files | Untrusted | Data only; schema-validated before any action |
| Host / Jarvis | Out of scope for AURA authority | AURA never touches it (ADR-0014) |

## 3. Threats and P1 controls
| # | Threat | Control | Test (D.1 P1) |
|---|---|---|---|
| T1 | Worker or model requests a tool/argument outside scope | Opaque token → Core grant registry → manifest tier → argument scope; deny + audit | capability test |
| T2 | SSRF via `web.fetch` (private, loopback, link-local, 169.254.169.254, IPv6 ULA/mapped, DNS rebinding, redirects, proxies) | Resolve once, check every resolved IP, dial the checked IP (custom `DialContext`), re-check on each redirect (max 5), ignore `HTTP(S)_PROXY`, https/http only on 80/443, allowlist per task, size 2 MB / 20 s caps | SSRF tests |
| T3 | Path traversal / symlink escape in workspace | Canonicalize, reject absolute and `..`, `openat`-style walk refusing symlinks, temp file + fsync + atomic rename, record path + sha256 | path tests |
| T4 | Prompt injection in fetched content changes policy/tools | Content wrapped as untrusted observation; policy/manifest/grants never derived from model or content; tool calls re-validated by Core | injection baseline |
| T5 | Secret leakage to worker/logs/events/prompts | Secrets read from files by Core only; worker env allowlist; redaction filter on all logs/events; secret scan of logs, events, worker `/proc/<pid>/environ` | secret scan |
| T6 | Overspend | Atomic reservation before each provider call against task + daily budget in one write transaction; reconcile after; unknown cost → fail closed | concurrent budget test |
| T7 | Duplicate side effects after crash | `STEP_STARTED` + idempotency key persisted before execution; recovery by side-effect class | 20× kill/restart |
| T8 | Approval tampering / replay | Canonical action hash, expiry, single-use, invalidation on change | approval integrity (SEC-08) |
| T9 | Runaway task / need to stop everything | Kill switch: cancel contexts of in-flight HTTP, SIGTERM→SIGKILL worker, block new calls, persist state | kill-switch test |
| T10 | API abuse | Loopback bind, 256-bit token in 0400 file, constant-time compare, per-token rate limit, request size caps, request IDs, structured errors without internals | API tests |
| T11 | Noisy neighbour with Jarvis | Container CPU/RAM/pids limits, one worker, one task at a time | resource baseline |

## 4. Honest limits in P1
- The worker is **not OS-sandboxed** and shares the Core uid inside the container (ADR-0007). Mitigated by
  architecture and tests; OS isolation is P4.
- Docker is a deployment boundary from Jarvis, not a hostile-code sandbox.
- The provider key is shared with Jarvis (A-04): AURA's budget bounds AURA's calls, not Jarvis's.
- No encryption at rest for secrets in P1 (file permissions only); encrypted storage and rotation are P4.

## 5. Permanent prohibitions (§3.1)
No unrestricted LLM-generated code or shell; no offensive operations; no CAPTCHA/MFA/paywall/anti-bot bypass; no
secret recording; no change of permissions, policy, identity or Charter by any model, document, or worker.
