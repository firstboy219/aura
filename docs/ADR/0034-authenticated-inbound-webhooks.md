# ADR-0034 — Authenticated inbound webhooks

Status: **Proposed** (2026-11-10). Blueprint §8.11 and §8.2.1 call for authenticated inbound webhooks in P3a (one scheduler; prefer
events over polling).

## Decision
- Config `webhooks: [{name, secret_file, event_key}]`. Each secret is at least 32 bytes in a file that only the Core reads. A missing
  or short secret stops startup.
- `POST /api/v1/webhooks/{name}` uses no bearer token, because external systems must not hold the owner's token. The request must
  carry:
  - `X-Aura-Timestamp`: unix seconds, accepted within ±5 minutes;
  - `X-Aura-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>`, checked in constant time;
  - optional `X-Aura-Delivery`, used for deduplication. Without it, the timestamp and body are used, so a replayed request is a
    duplicate.
- The body must be JSON and at most 16 KiB. It becomes the payload of the webhook's **fixed** `event_key`, through the same external
  event intake as `POST /events`. Tasks waiting for that key wake exactly once.
- A webhook grants nothing. It cannot start, change or approve a task, and its payload reaches later steps only as untrusted
  observation data.
- Every accepted webhook and every authentication failure is audited. The shared API rate limit applies.

## Consequences
The API binds to loopback (§11). Receiving webhooks from the internet therefore needs a TLS reverse proxy, which is the owner's
decision; nothing is exposed by default. Tests: `app/webhook_test.go` (valid, replay, wrong secret, stale, future, bearer only,
tampered, unknown, oversized, non-JSON, audit).
