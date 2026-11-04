-- Wave 23 slice 27: queue times between operations (PRD 4.23.3 job scheduling).
--
-- queue_before_minutes is the wait in front of an operation and
-- queue_after_minutes the wait behind it, in elapsed (real time) minutes. No
-- resource is booked while an operation queues; the next operation of the
-- order simply cannot start before the previous one ended plus its queue after
-- plus its own queue before. Existing operations keep zero (no wait). No new
-- table: the grants of 0001 cover scheduling_operation.
SET LOCAL lock_timeout = '5s';

ALTER TABLE scheduling_operation
  ADD COLUMN IF NOT EXISTS queue_before_minutes integer NOT NULL DEFAULT 0 CHECK (queue_before_minutes BETWEEN 0 AND 10080),
  ADD COLUMN IF NOT EXISTS queue_after_minutes integer NOT NULL DEFAULT 0 CHECK (queue_after_minutes BETWEEN 0 AND 10080);
