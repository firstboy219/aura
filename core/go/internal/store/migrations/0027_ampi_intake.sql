-- ODI (AMPI → AURA, ADR-0080): perception events delivered by AMPI are kept as untrusted EVIDENCE for AURA to use, not as instructions and not as memory of the owner.
-- Additive: one table. Retention is short (14 days) and enforced on every intake and by the daily cleanup.
CREATE TABLE ampi_events (
  event_id TEXT PRIMARY KEY,                -- AMPI's globally unique event id: a replay or a duplicate delivery is stored once
  subscription_id TEXT NOT NULL,
  sequence INTEGER NOT NULL,                -- per-subscription order as AMPI numbered it
  replay_gen INTEGER NOT NULL DEFAULT 0,
  topic TEXT NOT NULL,
  tenant_ref TEXT,
  session_ref TEXT,
  job_ref TEXT,
  job_status TEXT,
  n_facts INTEGER NOT NULL DEFAULT 0,
  n_unknown INTEGER NOT NULL DEFAULT 0,
  n_contradictions INTEGER NOT NULL DEFAULT 0,
  untrusted_content INTEGER NOT NULL DEFAULT 0,   -- 1 when the packet carries text read from media (OCR/transcript): data, never instructions
  bytes INTEGER NOT NULL,
  packet_json TEXT NOT NULL,
  received_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL);
CREATE INDEX ampi_events_time ON ampi_events(received_at);
CREATE INDEX ampi_events_session ON ampi_events(session_ref, received_at);
