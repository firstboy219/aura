# AURA incident runbooks (ODI A9)

Tested where marked **[tested]**; the rest are procedures to rehearse (Blueprint §20: "test the runbook rather than treating documentation as proof"). Commands run on the VPS as a user with `sudo`. Never paste a secret into a ticket or a chat.

## 0. Before any release
1. Backup: JARVIS side `sudo systemctl start jarvis-backup.service`; AURA side an online copy: `sudo python3 -c "import sqlite3; s=sqlite3.connect('file:/var/lib/docker/volumes/aura-data/_data/aura.db?mode=ro',uri=True); d=sqlite3.connect('/var/backups/odi-baseline/<date>/aura.db'); s.backup(d)"`; check `pragma integrity_check`. **[tested 2026-11-16]**
2. Keep the running image: `sudo docker tag aura-core:local aura-core:rollback-<date>` (release.sh rebuilds `aura-core:local`).
3. Tests green: `sudo ./dev test-go`, `sudo ./dev lint`, and `sudo ./dev test-py`.
4. After the deploy: `sudo bash deploy/checks/worker-isolation.sh` must say PASS **[tested]**; `GET /api/v1/system/health`; `GET /api/v1/admin/audit/verify` must be `valid: true`.

## 1. Roll back an AURA release
Code only: `sudo docker tag aura-core:rollback-<date> aura-core:local && cd ~/aura/deploy && sudo docker compose up -d`. The additive migrations (0019–0023) leave columns and tables the old binary ignores; if the old binary refuses the newer schema, restore the pre-migration backup the store wrote to `/var/lib/aura/backups` (writes made since the deploy are lost — say so to the owner first). See the rollback rehearsal in `AURA_COMPLETION_REPORT.md`.

## 2. NEXUS unavailable or misbehaving
`GET /api/v1/admin/nexus`: health state and the evidence. Mode `off` or `shadow`: nothing changes for the owner. Mode `primary` and `UNAVAILABLE`: fallback is allowed only if the owner named providers; look at `active_fallbacks`. To stop all use of NEXUS now: `POST /api/v1/admin/nexus/owner-disable {"minutes": 60}`; to return: `{"minutes": 0}` (revokes every fallback authorization). Last resort: `PUT /api/v1/admin/nexus {"mode":"off"}`.

## 3. Unexpected external-provider call
Audit: `GET /api/v1/admin/audit?type=FALLBACK_USED,FALLBACK_AUTHORIZED,TEACHER_ASKED,PROVIDER_REFUSED`. Revoke: `owner-disable` then `minutes: 0` (revokes authorizations). Check `usage_records` for providers other than the configured ones. Rotate the provider key only after every consumer is mapped (the OpenRouter key is shared with Jarvis).

## 4. Audit chain broken (`/admin/audit/verify` returns 409)
Stop making changes. Keep the database file and the last backup. The response names the first broken row. Compare the `head` with the anchored copy (if the owner keeps one). Treat it as a security incident: who had database access, restore from the last backup whose chain verified, and record the incident.

## 5. Worker isolation violation or probe failure
`sudo bash deploy/checks/worker-isolation.sh` FAIL → stop the worker (`sudo docker stop aura-worker`), keep its logs (`docker logs aura-worker`), check `docker inspect` for `network_mode=none`, `ReadonlyRootfs=true`, `CapDrop=[ALL]`, mounts only the socket; redeploy from the last good image.

## 6. Device lost or compromised
Owner: `POST /api/v1/devices/{id}/revoke {"reason":"..."}`. Its leases end; the tasks it held stay paused until you decide. Pair a new device (`POST /devices/pairings`). The old key cannot be revived.

## 7. Credential suspected compromised
Provider key: rotate with the provider, then update the vault secret (`PUT /api/v1/admin/credentials/...` or `aura secret`), restart, and tell the other consumers (JARVIS). Owner TOTP/web: re-run the setup link. Device keys: revoke the device. Service tokens (`connector.nexus_service_token`, `connector.teacher_service_token`): replace in the vault; NEXUS must receive the new one.

## 8. Restore AURA from backup
Stop the Core (`sudo docker stop aura-core`), copy the verified `aura.db` into the `aura-data` volume (keep the damaged one), start, then run `/api/v1/admin/audit/verify` and the health endpoint. Restore drill on a copy: see the completion report. Decide RPO/RTO with the owner (not set).

## 9. Disk full / SQLite contention
`GET /api/v1/system/resources`; free space first (old backups, build cache), never delete `aura.db` or `-wal`. Contention shows up as slow writes; the single-writer queue serializes them (`store.QueueDepth`).

## 10. Owner emergency stop
`POST /api/v1/system/stop` (kill switch): stops scheduling, cancels workers, closes browser sessions. R4 actions stay denied.
