-- Workspace Web App owner authentication (Blueprint 13A.4, D.1 P5, ADR-0041).
-- One owner. The password is an argon2id hash; the TOTP secret lives in the vault, not here.
CREATE TABLE web_owner (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  password_hash TEXT NOT NULL,
  totp_last_step INTEGER NOT NULL DEFAULT 0,      -- anti-replay: a code is accepted once
  failed_logins INTEGER NOT NULL DEFAULT 0, locked_until INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
-- Sessions are stored by the sha256 of the cookie value; the cookie itself is never stored.
CREATE TABLE web_sessions (
  id_hash TEXT PRIMARY KEY, csrf TEXT NOT NULL,
  created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, last_seen INTEGER NOT NULL,
  remote TEXT NOT NULL DEFAULT '', user_agent TEXT NOT NULL DEFAULT '');
-- One-time setup links (owner first login, password reset): stored by hash, short-lived, single use.
CREATE TABLE web_setup (
  token_hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL, used_at INTEGER,
  password_hash TEXT, totp_pending TEXT,             -- totp_pending: vault-encrypted until confirmed
  created_at INTEGER NOT NULL);
