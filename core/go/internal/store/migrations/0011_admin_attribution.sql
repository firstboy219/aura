-- Enterprise Admin (Blueprint D.1 P6, ADR-0049): members belong to a department, and each task records who created it,
-- so usage can be reported by member and department. Audit search/export reads audit_events (unchanged, append-only).
ALTER TABLE members ADD COLUMN department TEXT;
ALTER TABLE tasks ADD COLUMN created_by TEXT;
CREATE INDEX tasks_created_by ON tasks(created_by);
CREATE INDEX audit_at ON audit_events(at);
