-- #480: the shipped weight of a catch-weight packing slip line. qty stays the
-- shipped cases (the sales quantity); weight is the inventory quantity the
-- line's cost is taken on (weight x cost per weight unit), kept so the
-- cancellation mirrors it. NULL on a plain line. packing_slip_line is a live
-- table: a nullable column without a default only changes the catalog.
SET LOCAL lock_timeout = '5s';

ALTER TABLE packing_slip_line ADD COLUMN IF NOT EXISTS weight numeric(20,6) CHECK (weight IS NULL OR weight <> 0);
