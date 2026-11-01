-- Wave 16 slice 15: planned receipt date on transfer orders (FR-16.15). Additive: one
-- nullable column, checked against the planned ship date by the service at creation (no
-- table check: ship_date becomes the actual date at shipment and may pass the plan).
-- Partial shipments need no schema change: shipped_qty on the line accumulates across
-- shipments. No new table, so no new grant (transfer_order is already granted SELECT,
-- INSERT and UPDATE to passage_app).
ALTER TABLE transfer_order ADD COLUMN IF NOT EXISTS planned_receipt_date date;
