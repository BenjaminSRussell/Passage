-- Wave 16 slice 25: an ORDERED reservation (a claim on an expected receipt) becomes a PHYSICAL one when
-- the receipt arrives. converted_at records when, and tells a replayed request that a reservation which now
-- reads PHYSICAL was requested against the expected receipt. No new table, so no new grant:
-- inventory_reservation already carries SELECT, INSERT, UPDATE for passage_app from 0002.
--
-- inventory_reservation is a live table; a nullable column without a default is metadata-only. The
-- lock_timeout makes the migration fail (and the rollout retry) instead of queueing behind a long transaction.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_reservation ADD COLUMN IF NOT EXISTS converted_at timestamptz;
