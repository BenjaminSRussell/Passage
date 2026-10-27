-- Fulfillment slice 2: container types, packing rules and auto-pack (FR-28.15),
-- packaging materials per product, country fee rates and the statistical
-- packaging fee lines of a packing slip (FR-28.14), and returnable transport
-- packaging balances per customer (FR-28.16). Every table is row-level secured
-- by tenant_id; tables that carry legal_entity_id also get the restrictive
-- legal-entity scope policy. Weights and quantities are numeric(20,6), the
-- precision the API accepts, so Postgres never rounds a value silently.

-- A container records the weights and the packaging of its type when it is
-- packed, so a later change to the type does not rewrite shipped history.
ALTER TABLE container ADD COLUMN IF NOT EXISTS tare_weight_kg numeric(20,6) NOT NULL DEFAULT 0 CHECK (tare_weight_kg >= 0);
ALTER TABLE container ADD COLUMN IF NOT EXISTS gross_weight_kg numeric(20,6) NOT NULL DEFAULT 0 CHECK (gross_weight_kg >= 0);
ALTER TABLE container ADD COLUMN IF NOT EXISTS returnable boolean NOT NULL DEFAULT false;
ALTER TABLE container ADD COLUMN IF NOT EXISTS packaging_material_code text;

CREATE TABLE IF NOT EXISTS container_type (
  tenant_id uuid NOT NULL,
  code text NOT NULL CHECK (char_length(code) BETWEEN 1 AND 64),
  tare_weight_kg numeric(20,6) NOT NULL CHECK (tare_weight_kg >= 0),
  returnable boolean NOT NULL DEFAULT false,
  packaging_material_code text CHECK (packaging_material_code IS NULL OR char_length(packaging_material_code) BETWEEN 1 AND 32),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code)
);

CREATE TABLE IF NOT EXISTS packing_rule (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  product_id uuid NOT NULL,
  customer_id uuid,
  container_type text NOT NULL,
  qty_per_container numeric(20,6) NOT NULL CHECK (qty_per_container > 0),
  max_weight_kg numeric(20,6) NOT NULL CHECK (max_weight_kg > 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, container_type) REFERENCES container_type (tenant_id, code)
);
-- One rule per product and customer scope; NULL customer is the product's general rule.
CREATE UNIQUE INDEX IF NOT EXISTS packing_rule_scope_uq ON packing_rule (tenant_id, product_id,
  coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid));

CREATE TABLE IF NOT EXISTS product_packaging (
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  material_code text NOT NULL CHECK (char_length(material_code) BETWEEN 1 AND 32),
  weight_per_unit_kg numeric(20,6) NOT NULL CHECK (weight_per_unit_kg >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, product_id, material_code)
);

CREATE TABLE IF NOT EXISTS packaging_fee_rate (
  tenant_id uuid NOT NULL,
  country_code char(2) NOT NULL CHECK (country_code ~ '^[A-Z]{2}$'),
  material_code text NOT NULL CHECK (char_length(material_code) BETWEEN 1 AND 32),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  rate_per_kg numeric(20,6) NOT NULL CHECK (rate_per_kg >= 0),
  valid_from date NOT NULL,
  PRIMARY KEY (tenant_id, country_code, material_code, valid_from)
);

-- Statistical packaging fee lines of a packing slip (BR-28.12). A reversing
-- slip carries the mirror lines with negative weight and fee.
CREATE TABLE IF NOT EXISTS packaging_fee_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  packing_slip_id uuid NOT NULL,
  country_code char(2) NOT NULL,
  period char(7) NOT NULL CHECK (period ~ '^[0-9]{4}-[0-9]{2}$'),
  material_code text NOT NULL,
  weight_kg numeric(20,6) NOT NULL,
  rate_per_kg numeric(20,6) NOT NULL,
  fee_amount numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, packing_slip_id) REFERENCES packing_slip (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS packaging_fee_line_period_idx ON packaging_fee_line (tenant_id, country_code, period);
CREATE INDEX IF NOT EXISTS packaging_fee_line_slip_idx ON packaging_fee_line (tenant_id, packing_slip_id);

-- Returnable containers a customer holds, per container type (BR-28.13). The
-- balance never goes below zero.
CREATE TABLE IF NOT EXISTS returnable_packaging_balance (
  tenant_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  container_type text NOT NULL,
  balance numeric(20,6) NOT NULL DEFAULT 0 CHECK (balance >= 0),
  last_shipped_at timestamptz,
  last_returned_at timestamptz,
  PRIMARY KEY (tenant_id, customer_id, container_type),
  FOREIGN KEY (tenant_id, container_type) REFERENCES container_type (tenant_id, code)
);

ALTER TABLE container_type ENABLE ROW LEVEL SECURITY;
ALTER TABLE container_type FORCE ROW LEVEL SECURITY;
CREATE POLICY container_type_tenant_access ON container_type AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE packing_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE packing_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY packing_rule_tenant_access ON packing_rule AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE product_packaging ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_packaging FORCE ROW LEVEL SECURITY;
CREATE POLICY product_packaging_tenant_access ON product_packaging AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE packaging_fee_rate ENABLE ROW LEVEL SECURITY;
ALTER TABLE packaging_fee_rate FORCE ROW LEVEL SECURITY;
CREATE POLICY packaging_fee_rate_tenant_access ON packaging_fee_rate AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE packaging_fee_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE packaging_fee_line FORCE ROW LEVEL SECURITY;
CREATE POLICY packaging_fee_line_tenant_access ON packaging_fee_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY packaging_fee_line_legal_entity_scope ON packaging_fee_line AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE returnable_packaging_balance ENABLE ROW LEVEL SECURITY;
ALTER TABLE returnable_packaging_balance FORCE ROW LEVEL SECURITY;
CREATE POLICY returnable_packaging_balance_tenant_access ON returnable_packaging_balance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
