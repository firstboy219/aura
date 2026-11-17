-- ODI (JARVIS blueprint v1.4, ADR-0081): scoped spatial sessions authorized by AURA. Additive: one table. No frames, no precise location, no scene content is ever stored here:
-- only what was authorized (purpose, scope, retention class, expiry) and how the session ended. The route is off until the owner enables it (settings key spatial.sessions_enabled).
CREATE TABLE spatial_sessions (
  id TEXT PRIMARY KEY,
  purpose TEXT NOT NULL,
  modalities TEXT NOT NULL,            -- JSON array of the authorized modalities (v1: image, location)
  capture_scope TEXT NOT NULL,         -- JSON array of the authorized scope labels
  device_manifest TEXT NOT NULL,       -- JSON object, capability NAMES and booleans only (≤ 2 KiB)
  retention_class TEXT NOT NULL,       -- v1: session_ephemeral only
  consent_ref TEXT NOT NULL,
  foreground INTEGER NOT NULL,         -- v1: background capture is refused, so always 1
  state TEXT NOT NULL,                 -- authorized | stopped | revoked | expired
  state_reason TEXT,
  requested_by TEXT NOT NULL,
  ampi_session_ref TEXT,               -- NULL until an AMPI client credential exists and a session was minted
  ampi_state TEXT NOT NULL DEFAULT 'not_connected',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ended_at INTEGER);
CREATE INDEX spatial_sessions_state ON spatial_sessions(state, expires_at);
