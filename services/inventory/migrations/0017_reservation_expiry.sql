-- Wave 16 slice 16: reservation time to live (FR-16.7). Additive: one nullable column on
-- inventory_reservation (expires_at, set when the request names ttlHours) and a partial
-- index for the expiry run. No new table, so no new grant (inventory_reservation is already
-- granted SELECT, INSERT and UPDATE to passage_app).
ALTER TABLE inventory_reservation ADD COLUMN IF NOT EXISTS expires_at timestamptz;
CREATE INDEX IF NOT EXISTS inventory_reservation_expiry_idx
  ON inventory_reservation (tenant_id, expires_at) WHERE status = 'ACTIVE' AND expires_at IS NOT NULL;
