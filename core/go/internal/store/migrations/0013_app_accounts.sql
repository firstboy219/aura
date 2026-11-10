-- App accounts (ADR-0053): username + password sign-in for the Android client. Each sign-in issues a device token, stored
-- as a member row (token hash only) linked to the account, so it can be listed and revoked like any member.
CREATE TABLE app_accounts (
  id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 40),
  password_hash TEXT NOT NULL, role TEXT NOT NULL CHECK (role IN ('viewer','operator','approver')),
  failed_logins INTEGER NOT NULL DEFAULT 0, locked_until INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, disabled_at INTEGER);
ALTER TABLE members ADD COLUMN account_id TEXT REFERENCES app_accounts(id);
