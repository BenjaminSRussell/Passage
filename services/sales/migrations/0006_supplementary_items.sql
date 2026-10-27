-- Wave 27 slice 4: supplementary item rules (FR-27.14, AX PriceDiscSuppItem).
-- A rule adds quantity_per_base of the supplementary product for every whole
-- base_quantity of the parent product on a line, for one customer or (customer
-- NULL) every customer of the legal entity. A customer rule takes precedence
-- over the all-customers rule for the same supplementary product.

CREATE TABLE IF NOT EXISTS supplementary_item_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  parent_product_id uuid NOT NULL,
  customer_id uuid,
  supplementary_product_id uuid NOT NULL,
  unit text NOT NULL CHECK (length(unit) BETWEEN 1 AND 10),
  requirement text NOT NULL CHECK (requirement IN ('MANDATORY', 'OPTIONAL')),
  base_quantity numeric(20,6) NOT NULL CHECK (base_quantity > 0),
  quantity_per_base numeric(20,6) NOT NULL CHECK (quantity_per_base > 0),
  pricing text NOT NULL CHECK (pricing IN ('FREE', 'PRICED')),
  unit_price numeric(20,6) NOT NULL DEFAULT 0 CHECK (unit_price >= 0),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (pricing <> 'FREE' OR unit_price = 0),
  CHECK (parent_product_id <> supplementary_product_id)
);
CREATE INDEX IF NOT EXISTS supplementary_item_rule_parent ON supplementary_item_rule (tenant_id, legal_entity_id, parent_product_id);
-- One rule per (parent, customer or all customers, supplementary product), so
-- evaluation never adds the same item twice.
CREATE UNIQUE INDEX IF NOT EXISTS supplementary_item_rule_unique ON supplementary_item_rule
  (tenant_id, legal_entity_id, parent_product_id, coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid), supplementary_product_id);

ALTER TABLE supplementary_item_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplementary_item_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY supplementary_item_rule_tenant_access ON supplementary_item_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY supplementary_item_rule_legal_entity_scope ON supplementary_item_rule AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
