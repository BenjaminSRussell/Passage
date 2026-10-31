-- Wave 23 slice 10: backward scheduling (FR-23.2, BR-23.2).
--
-- A schedule run may now be backward as well as forward. No table is added,
-- so no new grants are needed.

ALTER TABLE schedule_run DROP CONSTRAINT IF EXISTS schedule_run_direction_check;
ALTER TABLE schedule_run ADD CONSTRAINT schedule_run_direction_check CHECK (direction IN ('forward', 'backward'));
