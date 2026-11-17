-- ODI end-to-end perception (JARVIS → AURA → AMPI → AURA → NEXUS → JARVIS). One row per owner request: what was asked, the AMPI refs, the bounded evidence, NEXUS's answer and the time
-- each hop took. The media itself is never stored here (it goes to AMPI, which keeps it under its own retention).
CREATE TABLE perception_requests (
  id TEXT PRIMARY KEY,
  modality TEXT NOT NULL CHECK (modality IN ('audio','image')),
  question TEXT NOT NULL,
  consent_ref TEXT NOT NULL,
  requested_by TEXT NOT NULL,
  state TEXT NOT NULL,              -- received | perceiving | reasoning | answered | failed
  step TEXT,
  ampi_session TEXT,
  ampi_job TEXT,
  ampi_status TEXT,
  evidence_json TEXT,
  answer TEXT,
  model TEXT,
  timings_json TEXT NOT NULL DEFAULT '{}',
  error TEXT,
  media_bytes INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
CREATE INDEX perception_requests_time ON perception_requests(created_at);
