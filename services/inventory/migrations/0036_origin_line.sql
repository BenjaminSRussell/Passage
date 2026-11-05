-- #480: a tracked receipt or delivery line is booked as one transaction per
-- serial or batch entry, each under its own origin (one origin registers one
-- transaction). line_source_id names the document line those entries belong
-- to, so a cancellation finds every entry of the line it offsets. NULL for
-- every other origin: an untracked line is its own origin.
-- inventory_transaction_origin is a live table: a nullable column without a
-- default only changes the catalog, and the lock_timeout keeps the migration
-- from queueing behind a long transaction. The index is built in 0037.
SET LOCAL lock_timeout = '5s';

ALTER TABLE inventory_transaction_origin ADD COLUMN IF NOT EXISTS line_source_id uuid;
