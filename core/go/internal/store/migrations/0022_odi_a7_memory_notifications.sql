-- ODI A7 (ADR-0076). Additive: nullable columns and one trigger. Nothing is rewritten or deleted.
-- Memories may expire (expires_at); an expired memory is retired by the maintenance tick (status 'deleted', lineage kept), never used as a fact again.
ALTER TABLE memories ADD COLUMN expires_at INTEGER;
-- Notifications: urgency, snooze, acknowledgement and expiry (Blueprint §17).
ALTER TABLE notifications ADD COLUMN urgency TEXT;
ALTER TABLE notifications ADD COLUMN snoozed_until INTEGER;
ALTER TABLE notifications ADD COLUMN acked_at INTEGER;
ALTER TABLE notifications ADD COLUMN expires_at INTEGER;
-- A notification that repeats an UNREAD one (same kind, title, trigger and task) within 24 h is dropped: no duplicate alerts.
CREATE TRIGGER notifications_no_duplicate_unread BEFORE INSERT ON notifications
WHEN EXISTS (SELECT 1 FROM notifications n WHERE n.kind = NEW.kind AND n.title = NEW.title AND n.read_at IS NULL
             AND n.trigger_id IS NEW.trigger_id AND n.task_id IS NEW.task_id AND n.created_at > NEW.created_at - 86400000)
BEGIN SELECT RAISE(IGNORE); END;
