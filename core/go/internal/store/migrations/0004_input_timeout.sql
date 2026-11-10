-- Clarification timeout policy (Blueprint §8.2, ADR-0032): reminders and INPUT_TIMEOUT.
CREATE TABLE task_inputs_new (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), step_id TEXT,
  question TEXT NOT NULL, answer TEXT,
  status TEXT NOT NULL CHECK (status IN ('pending','answered','cancelled','timed_out')),
  created_at INTEGER NOT NULL, answered_at INTEGER, reminded_at INTEGER);
INSERT INTO task_inputs_new(id, task_id, step_id, question, answer, status, created_at, answered_at)
  SELECT id, task_id, step_id, question, answer, status, created_at, answered_at FROM task_inputs;
DROP TABLE task_inputs;
ALTER TABLE task_inputs_new RENAME TO task_inputs;
CREATE INDEX inputs_task ON task_inputs(task_id, status);
CREATE INDEX inputs_pending ON task_inputs(status, created_at);
