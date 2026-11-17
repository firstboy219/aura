# ADR-0075 — ODI A6: device registry, pairing, signed requests, revocation, takeover leases

Status: Accepted by the owner 2026-11-17 ("Ok setuju": bulk approval of ADRs already implemented in running code, list in docs/odi/ADR_APPROVAL_CANDIDATES.md)

## Context
Blueprint v1.1 §12–§13 and AT-08/AT-11. A0: no device registry or pairing (only `members`, `app_accounts` and web sessions); browser takeover exists (`BrowserTakeover`/`BrowserReturn`, which pause the task and invalidate page refs so AURA must inspect again) but there is nothing for a paired local desktop/device worker, and nothing revoked a device independently of the user.

## Decision (migration 0021, additive: new tables only)
1. **Pairing**: the owner starts a pairing (`POST /devices/pairings`, capabilities from a proposed allowed set) and gets a one-time challenge (stored only as a hash, ≤15 min). The device completes it (`/devices/pairings/{id}/complete`, public, rate limited) by presenting its ed25519 public key **and a signature over the challenge proving it holds the private key**. The device is registered as `pending` and can do nothing.
2. **Owner confirmation and consent**: `POST /devices/{id}/confirm` makes it `trusted` with exactly the capabilities the owner consents to, a subset of what was requested (never more). On the web this needs the same TOTP step-up as an approval. A revoked device cannot be confirmed again.
3. **Signed requests**: every device call carries `X-Aura-Device`, `X-Aura-Timestamp` (ms), `X-Aura-Nonce` and an ed25519 signature over method, path, timestamp, nonce and body hash. Wrong key, tampered body or path, ±60 s clock skew, and a repeated nonce (replay; `device_nonces`, pruned after 5 min) are refused. IP address is not identity.
4. **Revocation** (`POST /devices/{id}/revoke`): trust ends at once, active leases are revoked, the tasks they held stay paused until the owner decides, the audit row records who and why. Device trust is independent of the owner's own login.
5. **Takeover lease**: a trusted device with `browser.control` (or `input.mouse`+`input.keyboard`) begins a lease on a task; automation on the task is paused (a task already paused for another reason is not marked as ours); a second lease on the same task conflicts; **handback needs the sha256 of a fresh observation**, only the holding device can release, and only then is a task we paused resumed. Overdue leases expire; their tasks stay paused.

## Not done
No desktop worker, no capability enforcement inside such a worker, no push channel; the web/Android apps are not changed; the existing browser takeover is unchanged (it already satisfies AT-11 for the browser). Public-key rotation, per-capability rate limits and a privacy indicator on the device are open items (owner decisions: supported desktop OS, pairing UX, permitted screen/input capabilities, privacy indicator).

## Rollback
Revert the commit; the new tables stay unused.
