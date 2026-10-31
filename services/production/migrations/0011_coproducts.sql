-- Wave 21 slice 12: co-products and by-products on the order and their RAF lines
-- (FR-21.14, quantity allocation only). Additive (expand): no existing row changes.

-- A co-product line is declared on the order at creation: the item, the planned
-- quantity per unit of the order's product and the planned and reported totals.
-- A co-product takes its share of the order's cost by quantity; a by-product
-- carries no cost allocation (BR-9.12) and is received at zero cost.
CREATE TABLE IF NOT EXISTS production_coproduct (
  tenant_id uuid NOT NULL,
  order_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  line_number text NOT NULL,
  coproduct_type text NOT NULL DEFAULT 'co-product' CHECK (coproduct_type IN ('co-product','by-product')),
  item_id uuid NOT NULL,
  qty_per numeric(18,6) NOT NULL CHECK (qty_per > 0),
  qty_planned numeric(18,6) NOT NULL CHECK (qty_planned > 0),
  qty_reported numeric(18,6) NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, order_id, line_number),
  FOREIGN KEY (tenant_id, order_id) REFERENCES production_order (tenant_id, id)
);

ALTER TABLE production_coproduct ENABLE ROW LEVEL SECURITY;
ALTER TABLE production_coproduct FORCE ROW LEVEL SECURITY;
CREATE POLICY production_coproduct_tenant_access ON production_coproduct AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY production_coproduct_legal_entity_scope ON production_coproduct AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The journal line that receipts a co-product names its co-product line.
ALTER TABLE production_journal_line ADD COLUMN IF NOT EXISTS coproduct_line_number text;
ALTER TABLE production_journal_line DROP CONSTRAINT IF EXISTS production_journal_line_kind_check;
ALTER TABLE production_journal_line ADD CONSTRAINT production_journal_line_kind_check
  CHECK (kind IN ('issue','hours','receipt','coproduct'));
ALTER TABLE production_journal_line DROP CONSTRAINT IF EXISTS production_journal_line_coproduct_check;
ALTER TABLE production_journal_line ADD CONSTRAINT production_journal_line_coproduct_check
  CHECK ((kind = 'coproduct') = (coproduct_line_number IS NOT NULL));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON production_coproduct TO passage_app';
  END IF;
END
$$;
