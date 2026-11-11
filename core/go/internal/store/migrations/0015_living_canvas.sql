-- Living canvas (ADR-0060). A reply carries a scene (mood, accent, layout). A screen is a page of the owner's app
-- that AURA designs as data: versions are kept, exactly one per name is active, and the owner can roll back.
ALTER TABLE task_replies ADD COLUMN scene_json TEXT NOT NULL DEFAULT '{}';
CREATE TABLE ui_screens (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  version INTEGER NOT NULL,
  spec_json TEXT NOT NULL,
  request TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  task_id TEXT REFERENCES tasks(id),
  active INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  UNIQUE(name, version));
CREATE UNIQUE INDEX ui_screens_active ON ui_screens(name) WHERE active = 1;
