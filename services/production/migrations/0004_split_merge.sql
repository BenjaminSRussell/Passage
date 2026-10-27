-- Wave 21 slice 4: order split and merge (FR-21.15). Additive: existing orders
-- were neither split nor merged.
-- split_from_order_id is the order a split took its quantity from;
-- merged_into_order_id is set on the cancelled orders of a merge (PRD 4.21.4).

ALTER TABLE production_order
  ADD COLUMN IF NOT EXISTS split_from_order_id uuid,
  ADD COLUMN IF NOT EXISTS merged_into_order_id uuid;

ALTER TABLE production_order DROP CONSTRAINT IF EXISTS production_order_split_from_fk;
ALTER TABLE production_order ADD CONSTRAINT production_order_split_from_fk
  FOREIGN KEY (tenant_id, split_from_order_id) REFERENCES production_order (tenant_id, id);
ALTER TABLE production_order DROP CONSTRAINT IF EXISTS production_order_merged_into_fk;
ALTER TABLE production_order ADD CONSTRAINT production_order_merged_into_fk
  FOREIGN KEY (tenant_id, merged_into_order_id) REFERENCES production_order (tenant_id, id);
-- Invariant: an order merged into another is Cancelled.
ALTER TABLE production_order DROP CONSTRAINT IF EXISTS production_order_merged_cancelled_check;
ALTER TABLE production_order ADD CONSTRAINT production_order_merged_cancelled_check
  CHECK (merged_into_order_id IS NULL OR status = 'Cancelled');

CREATE INDEX IF NOT EXISTS production_order_split_from
  ON production_order (tenant_id, split_from_order_id) WHERE split_from_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS production_order_merged_into
  ON production_order (tenant_id, merged_into_order_id) WHERE merged_into_order_id IS NOT NULL;
