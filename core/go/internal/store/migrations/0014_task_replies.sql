-- Owner replies (ADR-0055): what AURA tells the owner when a task ends — plain text plus an adaptive view (a list of
-- declarative blocks sanitised by the Core). One reply per root task; a re-run of the final phase replaces it.
CREATE TABLE task_replies (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id),
  text TEXT NOT NULL,
  view_json TEXT NOT NULL DEFAULT '[]',
  basis TEXT NOT NULL CHECK (basis IN ('evidence','model_knowledge','summary')),
  notes_json TEXT NOT NULL DEFAULT '[]',
  files_json TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL);
CREATE INDEX task_replies_created ON task_replies(created_at);
