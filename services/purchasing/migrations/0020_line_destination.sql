-- Wave 17 purchasing slice 16: delivery destination per line (FR-17.1, FR-17.3). A requisition line names
-- the warehouse it is wanted in; a PO line names the site and warehouse it is delivered to. On a PO line
-- an empty (NULL) value means "as on the PO header" and a stored value is an override that differs from
-- the header. The header site and warehouse are fixed from the first confirmation, so a line that follows
-- the header can only move with it before any receipt exists. Planning (wave 22) reads open lines by
-- warehouse through GET /purchase-order-lines. Additive only (expand/contract); no new tables, so the
-- passage_app grants on both tables stand.
--
-- purchase_order_line and purchase_requisition_line are live tables: nullable columns without a default
-- only change the catalog, so the ACCESS EXCLUSIVE lock lasts an instant and nothing is scanned or
-- rewritten. The lock_timeout makes the migration fail (and the rollout retry) instead of queueing behind
-- a long transaction while every later query queues behind it. 0021 builds the indexes.
SET LOCAL lock_timeout = '5s';

ALTER TABLE purchase_requisition_line ADD COLUMN IF NOT EXISTS warehouse_id uuid;
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS site_id uuid;
ALTER TABLE purchase_order_line ADD COLUMN IF NOT EXISTS warehouse_id uuid;
