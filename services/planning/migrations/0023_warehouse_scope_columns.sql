-- FR-22.18, BR-22.7, AC-22.16: an item is planned in the scope of a warehouse apart
-- from the site. The planned orders, pegs, action messages and net requirement rows of
-- a warehouse scope carry the warehouse; null is the site scope, so every row written
-- before this migration keeps its meaning. Catalog-only ALTERs: the nullable columns
-- have no default and no rewrite, but each takes ACCESS EXCLUSIVE for an instant, so a
-- long reader makes the migration fail fast (and retry) rather than queue every read
-- behind it. No new table (all four are under RLS and granted); the net requirement key
-- follows in 0024 and 0025.
SET LOCAL lock_timeout = '5s';

ALTER TABLE planned_order ADD COLUMN IF NOT EXISTS warehouse_id uuid;
ALTER TABLE pegging ADD COLUMN IF NOT EXISTS warehouse_id uuid;
ALTER TABLE action_message ADD COLUMN IF NOT EXISTS warehouse_id uuid;
ALTER TABLE net_requirement ADD COLUMN IF NOT EXISTS warehouse_id uuid;
