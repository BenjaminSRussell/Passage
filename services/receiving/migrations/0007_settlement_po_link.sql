ALTER TABLE receiving_settlement
  ADD COLUMN IF NOT EXISTS purchase_order_id uuid,
  ADD COLUMN IF NOT EXISTS po_line_no int,
  ADD COLUMN IF NOT EXISTS product_id uuid;

CREATE INDEX IF NOT EXISTS receiving_settlement_po_product_ix ON receiving_settlement (tenant_id, purchase_order_id, product_id) WHERE kind = 'PLAN_ROW';
