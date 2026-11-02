-- Wave 23 slice 20: production orders withdrawn from scheduling (FR-23.10).
--
-- When production resets an order to Estimated or Created, cancels it or merges
-- it into another order, its statuschanged event carries no dates: the order is
-- no longer scheduled. Scheduling withdraws the operations it registered for
-- the order (and their non-firmed jobs) and keeps the order's import row with
-- the new status 'withdrawn', when it was withdrawn and to which status, so a
-- planner sees why the order's operations are gone. order_event_at is the time
-- of the latest production event applied to the row (order.scheduled.v1 or the
-- withdrawing order.statuschanged.v1, which carries no order version): an event
-- older than it does not undo the newer one. No new table: the UPDATE grant of
-- 0013 covers the row change.

ALTER TABLE scheduling_order_import ADD COLUMN IF NOT EXISTS withdrawn_at timestamptz;
ALTER TABLE scheduling_order_import ADD COLUMN IF NOT EXISTS withdrawn_to text;
ALTER TABLE scheduling_order_import ADD COLUMN IF NOT EXISTS order_event_at timestamptz;

ALTER TABLE scheduling_order_import DROP CONSTRAINT IF EXISTS scheduling_order_import_status_check;
ALTER TABLE scheduling_order_import ADD CONSTRAINT scheduling_order_import_status_check
  CHECK (status IN ('imported', 'partial', 'rejected', 'withdrawn'));
