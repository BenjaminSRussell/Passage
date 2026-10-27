-- Fulfillment slice 6: the "short picks and backorders" report (FR-28.2,
-- section 5.28.14). A pick confirmation that leaves a line below its planned
-- quantity stamps the line with the time of its first short pick; the
-- remainder (planned - picked) is the backorder until a re-pick closes it or
-- the shipment is cancelled. The stamp stays once set, so the report can show
-- closed backorders by the date they were created and its keyset cursor is
-- stable. shipment_line keeps its tenant policy; the report reads lines
-- through their shipment, so the legal-entity scope of the shipment applies.

ALTER TABLE shipment_line ADD COLUMN IF NOT EXISTS short_picked_at timestamptz;

-- Lines already short-picked before this migration take their shipment's last
-- change as the short-pick time (the best record there is).
UPDATE shipment_line sl SET short_picked_at = sh.updated_at
FROM shipment sh
WHERE sh.tenant_id = sl.tenant_id AND sh.id = sl.shipment_id
  AND sl.pick_confirmed AND sl.qty_picked < sl.qty_planned AND sl.short_picked_at IS NULL;

CREATE INDEX IF NOT EXISTS shipment_line_short_pick_idx ON shipment_line (tenant_id, short_picked_at DESC, id DESC)
  WHERE short_picked_at IS NOT NULL;
