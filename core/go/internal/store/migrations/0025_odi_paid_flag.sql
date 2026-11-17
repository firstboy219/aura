-- ODI (owner decision 2026-11-17): the limits on NEXUS's use of external AI (budget, call counts, daily caps) apply to PAID routes only; free routes are unlimited.
-- These flags say which rows were served by a paid route, so only those count against the limits. Additive.
ALTER TABLE teacher_requests ADD COLUMN paid INTEGER NOT NULL DEFAULT 0;
ALTER TABLE teaching_examples ADD COLUMN paid INTEGER NOT NULL DEFAULT 0;
