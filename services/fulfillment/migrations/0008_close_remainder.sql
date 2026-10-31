-- Fulfillment slice 7: close the open remainder of a partially shipped shipment
-- (FR-28.8, short_pick_action CANCEL_REMAINDER). Closing shrinks every line to
-- what shipped and records the quantity given up in qty_closed, so a closed
-- line has qty_planned = qty_shipped (possibly 0) and no backorder. No new
-- table: shipment_line already has its tenant policy and grants.

ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS qty_closed numeric(20,6) NOT NULL DEFAULT 0 CHECK (qty_closed >= 0);
ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS closed_at timestamptz;

ALTER TABLE shipment_line DROP CONSTRAINT IF EXISTS shipment_line_qty_planned_check;
ALTER TABLE shipment_line ADD CONSTRAINT shipment_line_qty_planned_check CHECK (qty_planned > 0 OR qty_closed > 0);
