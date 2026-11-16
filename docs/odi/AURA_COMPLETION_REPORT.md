# AURA — ODI implementation report, phases A0–A9 (16 November 2026)

Per Blueprint v1.1 §27. Claims are labelled **implementation-verified** (observed test or probe output, listed below), **design-reviewed**, or **production-proven** (none yet: nothing is deployed at the time of writing; see "Release"). Nothing here certifies readiness for an owner-facing NEXUS: **no NEXUS exists yet**, so the NEXUS-facing code was exercised only against a fake.

## 1. What was built, by phase
| Phase | Change | ADR | Migration |
|---|---|---|---|
| A0 | Read-only re-audit: inventory, credential map, gap matrix | — | — |
| A1 | Contracts as files with tests: state mapping (12 persisted → 18 canonical), 26-code error catalogue, event envelope, NEXUS gateway contract, executable acceptance catalogue AT-01…12 | 0069, 0070 (design) | — |
| A2 | Derived canonical state, verification levels V0–V3, truthful claims; `assurance` on `GET /tasks/{id}`; reply note `unverified_outcome` | 0071 | — |
| A3 | Approval nonce/auth strength/risk label/revoke, tamper-evident audit hash chain + `/admin/audit/verify` | 0072 | 0019 |
| A4 | NEXUS gateway (`off`\|`shadow`\|`primary`, default **off**), AURA-owned fallback authorization with health hysteresis, Teacher Gate | 0073 | 0020 |
| A5 | Tool manifests with drift guard, per-tool daily quotas, isolation evidence | 0074 | — |
| A6 | Device registry, pairing with proof of key possession, signed requests, revocation, takeover leases | 0075 | 0021 |
| A7 | Trigger policy (overlap, quiet hours, expiry, max runs, grace, jitter), memory expiry, notification snooze/ack/dedup, retention dry-run | 0076 | 0022 |
| A8 | Self-engineering L0–L3 classification, weakening guard, approval rules, evidence-gated rollout | 0077 | 0023 |
| A9 | Runbooks, migration drill, rollback rehearsal, load test, supply-chain/build/Python/shell baselines, this report | — | — |

Size of the change set relative to `main` before A1…A9: 73 files, ≈6,750 lines added, 7 commits on the chain `main → odi/a2 … odi/a9`. Migrations 0019–0023 are additive: nullable columns, one trigger, new tables; nothing is rewritten or deleted.

## 2. Acceptance catalogue (Blueprint Appendix C), from `contracts/odi/acceptance-catalogue.json`
| AT | Status | Basis |
|---|---|---|
| AT-01 NEXUS-only | **met (against a fake NEXUS)** | `nexus.TestAT01…`: low confidence, insufficient evidence, overload, rate limit, deadline, bad request and a slow call never reach an external provider nor mark NEXUS unavailable |
| AT-02 fallback authorization | **met (fake)** | `TestAT02…`: evidence alone is not enough; no provider named / class not allowed / policy unreadable / health unknown / only degraded all deny; scoped, audited, never renewed once used up |
| AT-03 recovery | **met (fake)** | `TestAT03…`, `TestMonitorHysteresis`, `TestOwnerDisable…` |
| AT-04 approval binding | **met** | `policy.TestApprovalIntegrity` + API 409/410 |
| AT-05 worker isolation | **met** | live probe of `aura-worker` on 2026-11-16: every probe denied, only `lo`, uid 10002 (`docs/odi/evidence/`); `engine.TestOutOfScopeRequestsDeniedAndAudited` |
| AT-06 idempotency | **met** | API Idempotency-Key, webhook once, trigger once per slot, step keys, `TestCrashRecovery` |
| AT-07 prompt injection | **met** | provenance-denied URL from an injected snippet, hostile-worker test, A3 forbidden write, delegation widening denied |
| AT-08 revocation | **met** | grants, approvals (`TestRevokeApproval…`), devices (`TestRevocation…`, HTTP flow), cancel/kill |
| AT-09 truthful status | **met** | `odi.TestDeriveTruthfulClaims`, reply note |
| AT-10 recovery | **met** | `TestCrashRecovery` (20 random SIGKILLs of Core+worker) |
| AT-11 desktop takeover | **partial** | browser takeover exists and re-inspects on handback; device lease server side tested; **no desktop worker exists** |
| AT-12 restore | **partial** | the production database copy opens, migrates, passes `integrity_check`, row counts unchanged (below); a full restore into a running AURA container and RPO/RTO are not done: **owner decision** |

## 3. Verification (exact commands and results)
- `sudo ./dev test-go` (whole Go suite, official runner): green on the final tree: **29 packages OK, 0 failed**. Baseline on `main` before A1: 26 packages OK, 0 failures. `sudo ./dev lint`: clean. `sudo ./dev build` (vet + build): exit 0. `sudo ./dev test-py`: **81 passed** (after updating the planned-table inventory with the ODI tables, which that test requires; no assertion was weakened). `sudo ./dev test-shell`: **7 passed**. `sudo ./dev audit` (licences): 34 OK, 0 needing approval, 0 unknown, 0 forbidden.
- **Migration drill on a copy of the production database** (baseline backup, 14:38 UTC): migrated in **487 ms**, schema_migrations max 23, `integrity_check` ok, `foreign_key_check` 0, **every table's row count unchanged** (tasks 5,798; task_events 34,777; audit_events 9,708; approvals 22; skills 214; memories 318; …), audit chain valid with 9,708 legacy rows and 3 chained rows after new writes.
- **Rollback rehearsal**: the previous code (`5573790`) opens the migrated database and can still append to `audit_events`; so rolling back the *code* needs no database restore.
- **Load**: 1,800 audited writes by 12 writers plus 4 readers (931k reads) in 7.7 s: 235 writes/s, p50 50 ms, p95 65 ms, p99 75 ms, max 100 ms, **0 errors**, chain valid and gap-free.
- **Not run:** the full flow against a real NEXUS (none exists); a real fallback to Gemini/OpenRouter; a desktop worker; chaos/partition tests beyond the existing crash test.

## 4. Security implications
Strengthened: tamper-evident audit; approvals accountable and revocable; fallback only by AURA policy on evidence, default-deny, scoped and revoked on recovery; Teacher Gate behind its own token, sanitizer and owner opt-in; device identity with proof of possession, signed requests and instant revocation; L3 changes need the owner's second factor. New attack surface: public `POST /devices/pairings/{id}/complete` (one-time challenge, key proof, rate limited), `POST /teacher/requests` (own service token), signed `/devices/self/*`. Defaults deny: no fallback provider, no Teacher provider, NEXUS mode off, no quota, no device.

## 5. Operational commands
Deploy: see `docs/odi/RUNBOOKS.md` §0 (backup, tag the running image, `sudo deploy/release.sh <version>`, post-deploy checks). Owner controls: `GET/PUT /api/v1/admin/nexus`, `POST …/owner-disable`, `GET /admin/audit/verify`, `GET/PUT /admin/tool-quotas`, `GET /admin/tool-manifests`, `/devices/*`, `/admin/changes/*`, `GET /admin/retention`.

## 6. Rollback
Code: re-tag the saved image and `docker compose up -d` (the old binary runs on the migrated database, rehearsed). Data: nothing was deleted; the store also writes a pre-migration backup. Mode `off` restores pure legacy routing instantly.

## 7. Unresolved risks and owner decisions (not guessed)
1. **No NEXUS exists**: AT-01…03 are proven against a fake only; thresholds (3 failures/60 s, 2 successes + 30 s hold-down, 45 s staleness) are proposals.
2. Which providers may serve as fallback and for the Teacher Gate, with what budget and data classes (all default to none).
3. R0–R4 mapping is a proposal that only labels; approving or replacing it is the owner's.
4. Audit anchoring: the chain cannot detect deletion of the **tail**; the owner must choose where to anchor `head` (e.g. Jarvis database, e-mail).
5. RPO/RTO, restore cadence, off-site backup destination (none configured; all backups are on the same disk).
6. Core network egress is not allow-listed and the host firewall is off (AURA's own provider and web calls go straight out); per-destination allow-listing needs an owner-approved design.
7. `aura-shell` still mounts two JARVIS folders (read-only): keep or replace by an API.
8. Desktop worker, supported OS, pairing UX, privacy indicator: unbuilt and undecided.
9. Retention is a dry run only; enforcing it is destructive and needs the owner's numbers.
10. 49 ADRs remain "Proposed" although many run in production (`docs/odi/ADR_STATUS.md`), plus ADR-0069…0077 added here.
11. The OpenRouter key is shared with Jarvis (verified identical): a revocation or credit exhaustion hits both.

## 8. Next recommended phase
Build NEXUS mini and run AURA in `shadow` mode against it to gather real agreement/latency data before any move to `primary`; then decide the owner items above and re-run AT-01…03 against the real service.
