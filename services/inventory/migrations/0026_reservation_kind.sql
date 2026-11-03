-- Wave 16 slice 24: reservations against expected receipts (FR-16.7, RESERVED_ORDERED). A
-- reservation either holds physical stock (kind PHYSICAL, the reserved_physical bucket, as before)
-- or a quantity of the ordered / arrived supply of one dimension set (kind ORDERED, the
-- reserved_ordered bucket). No new table: inventory_reservation already carries the passage_app
-- grants (SELECT, INSERT, UPDATE) from 0002.
--
-- inventory_reservation is a live table, so this file only changes the catalog: a constant default is
-- metadata-only and the NOT VALID check never scans. The lock_timeout makes the migration fail (and
-- the rollout retry) instead of queueing behind a long transaction. 0027 validates the check.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_reservation ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'PHYSICAL';
ALTER TABLE inventory_reservation ADD CONSTRAINT inventory_reservation_kind_check
  CHECK (kind IN ('PHYSICAL', 'ORDERED')) NOT VALID;
