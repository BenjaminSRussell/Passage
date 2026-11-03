-- Wave 21 slice 24: validate the checks 0022 added NOT VALID. VALIDATE CONSTRAINT scans the
-- table under SHARE UPDATE EXCLUSIVE, which lets reads and writes go on; it runs in its own
-- migration (its own transaction) so it does not hold 0022's ACCESS EXCLUSIVE lock while it scans.
SET LOCAL lock_timeout = '5s';

ALTER TABLE production_trigger_point VALIDATE CONSTRAINT production_trigger_point_event_check;
ALTER TABLE production_trigger_firing VALIDATE CONSTRAINT production_trigger_firing_event_check;
ALTER TABLE production_trigger_firing VALIDATE CONSTRAINT production_trigger_firing_event_journal;
