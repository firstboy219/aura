-- Workspace (ADR-0061). Notes the owner asked AURA to keep, and pictures the Core found for the figures of an
-- explanation (stored once by content hash, with the page they illustrate). media_lookups remembers what a name
-- resolved to (NULL = nothing found) so the same name is not looked up again.
CREATE TABLE notes (
  id TEXT PRIMARY KEY,
  text TEXT NOT NULL,
  done INTEGER NOT NULL DEFAULT 0,
  task_id TEXT REFERENCES tasks(id),
  seq INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX notes_task ON notes(task_id, seq) WHERE task_id IS NOT NULL;
CREATE INDEX notes_created ON notes(created_at);
CREATE TABLE media (
  id TEXT PRIMARY KEY,
  content_type TEXT NOT NULL,
  bytes BLOB NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  source_url TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL);
CREATE TABLE media_lookups (
  name TEXT PRIMARY KEY,
  media_id TEXT REFERENCES media(id),
  created_at INTEGER NOT NULL);
