-- ODI A6 (ADR-0075). Additive: new tables only. Device trust is distinct from user identity and revocable on its own (Blueprint §13).
CREATE TABLE devices (
  id TEXT PRIMARY KEY,
  owner_ref TEXT NOT NULL,
  name TEXT NOT NULL,
  platform TEXT NOT NULL CHECK (platform IN ('android','web','desktop','other')),
  key_id TEXT NOT NULL UNIQUE,           -- first 16 hex chars of sha256(public key)
  public_key TEXT NOT NULL,              -- ed25519, base64
  capabilities_json TEXT NOT NULL,       -- what the owner consented to (a subset of what was requested)
  requested_json TEXT NOT NULL,
  trust TEXT NOT NULL CHECK (trust IN ('pending','trusted','revoked')),
  app_version TEXT,
  created_at INTEGER NOT NULL,
  paired_at INTEGER,
  last_seen_at INTEGER,
  revoked_at INTEGER,
  revoke_reason TEXT);
CREATE TABLE device_pairings (
  id TEXT PRIMARY KEY,
  challenge_sha256 TEXT NOT NULL,        -- the challenge is shown once and stored only as a hash
  owner_ref TEXT NOT NULL,
  capabilities_json TEXT NOT NULL,
  expires_at INTEGER NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('open','completed','expired','cancelled')),
  device_id TEXT REFERENCES devices(id),
  created_at INTEGER NOT NULL);
CREATE TABLE device_nonces (            -- replay protection for signed device requests
  device_id TEXT NOT NULL,
  nonce TEXT NOT NULL,
  at INTEGER NOT NULL,
  PRIMARY KEY (device_id, nonce));
-- A takeover lease: while it is active, automation on the task is paused; handing back needs a fresh observation (Blueprint §12).
CREATE TABLE device_leases (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES devices(id),
  task_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('takeover')),
  state TEXT NOT NULL CHECK (state IN ('active','released','revoked','expired')),
  paused INTEGER NOT NULL DEFAULT 0,       -- 1: this lease paused the task, so handback may resume it
  observation_sha256 TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  released_at INTEGER);
CREATE INDEX device_leases_active ON device_leases(task_id, state);
