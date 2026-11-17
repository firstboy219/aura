-- ODI (owner direction 2026-11-17: "heavy tasks go to external AI and NEXUS mini learns from them"), ADR-0078. Additive: one table.
-- A teaching example is what an external provider answered for a PUBLIC task (never private ones), kept so that NEXUS can learn from it through AURA.
CREATE TABLE teaching_examples (
  id TEXT PRIMARY KEY,
  purpose TEXT,
  tier TEXT,
  messages_json TEXT NOT NULL,
  schema_json TEXT,
  answer TEXT NOT NULL,
  provider TEXT,
  route TEXT,
  content_sha256 TEXT NOT NULL UNIQUE,   -- the same example is kept once
  tokens_est INTEGER,
  status TEXT NOT NULL CHECK (status IN ('new','sent','rejected')),
  reject_reason TEXT,
  created_at INTEGER NOT NULL,
  sent_at INTEGER);
CREATE INDEX teaching_status ON teaching_examples(status, created_at);
