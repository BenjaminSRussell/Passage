-- Fulfillment slice 9: the reason of a short pick (section 5.28.14, short picks
-- report "reason" column). No new table: shipment_line keeps its tenant policy
-- and its grants. NULL means no reason was given.

ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS short_pick_reason text;
ALTER TABLE shipment_line DROP CONSTRAINT IF EXISTS shipment_line_short_pick_reason_check;
ALTER TABLE shipment_line ADD CONSTRAINT shipment_line_short_pick_reason_check
  CHECK (short_pick_reason IS NULL OR short_pick_reason IN ('OUT_OF_STOCK', 'DAMAGED', 'NOT_FOUND', 'QUALITY_HOLD', 'OTHER'));
