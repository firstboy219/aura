-- Encrypted secrets at rest (Blueprint D.1 P4, ADR-0036). Values are AES-256-GCM ciphertext; the master key is a separate file.
CREATE TABLE secrets (
  name TEXT PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
  key_id TEXT NOT NULL, nonce BLOB NOT NULL, ciphertext BLOB NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
