-- Reminders the owner asked for in conversation (ADR-0063): one-shot (due_at) or daily (daily_at, workspace time
-- zone). The scheduler turns a due reminder into an owner notification; nothing else happens when it fires.
CREATE TABLE reminders (
  id TEXT PRIMARY KEY,
  text TEXT NOT NULL,
  due_at INTEGER,
  daily_at TEXT,
  next_at INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  task_id TEXT REFERENCES tasks(id),
  seq INTEGER NOT NULL DEFAULT 0,
  last_fired_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
CREATE UNIQUE INDEX reminders_task ON reminders(task_id, seq) WHERE task_id IS NOT NULL;
CREATE INDEX reminders_due ON reminders(enabled, next_at);
