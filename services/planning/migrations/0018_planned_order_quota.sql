-- FR-22.17, BR-22.12: a plan run applies the item's quota arrangement to its
-- planned orders. The order records the arrangement and the source it was
-- allocated to (a purchase order also gets the source's vendor in vendor_id);
-- firming books the quantity against the source (quota_source.allocated_qty).
-- No new table: both tables are already under RLS and granted to passage_app.
ALTER TABLE planned_order
  ADD COLUMN IF NOT EXISTS quota_arrangement_id uuid,
  ADD COLUMN IF NOT EXISTS quota_source_id uuid;
CREATE INDEX IF NOT EXISTS planned_order_quota_source_ix ON planned_order (tenant_id, quota_source_id)
  WHERE quota_source_id IS NOT NULL;
