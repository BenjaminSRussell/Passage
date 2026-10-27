-- Wave 27 slice 4: quotations with conversion to sales orders (FR-27.1,
-- BR-27.5). A quotation is numbered SQ- per legal entity; its lines convert,
-- whole or by line, into DRAFT sales orders, and each converted line records
-- the order it became.

CREATE TABLE IF NOT EXISTS sales_quotation (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  quotation_number text NOT NULL CHECK (quotation_number ~ '^SQ-[0-9]{7,}$'),
  customer_id uuid NOT NULL,
  invoice_account_id uuid NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SENT', 'CONVERTED', 'LOST', 'EXPIRED', 'CANCELLED')),
  probability_pct integer NOT NULL DEFAULT 50 CHECK (probability_pct BETWEEN 0 AND 100),
  valid_until date NOT NULL,
  net_total numeric(19,4) NOT NULL DEFAULT 0,
  lost_reason text CHECK (lost_reason IS NULL OR length(lost_reason) BETWEEN 1 AND 200),
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_by text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, legal_entity_id, quotation_number),
  CHECK (status <> 'LOST' OR lost_reason IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS sales_quotation_customer ON sales_quotation (tenant_id, legal_entity_id, customer_id);
CREATE INDEX IF NOT EXISTS sales_quotation_open ON sales_quotation (tenant_id, valid_until) WHERE status IN ('DRAFT', 'SENT');

CREATE TABLE IF NOT EXISTS sales_quotation_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  quotation_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  unit text NOT NULL CHECK (length(unit) BETWEEN 1 AND 10),
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  disc_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (disc_pct BETWEEN 0 AND 100),
  line_net numeric(19,4) NOT NULL,
  requested_ship_date date NOT NULL,
  line_status text NOT NULL DEFAULT 'OPEN' CHECK (line_status IN ('OPEN', 'CONVERTED')),
  converted_order_id uuid,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, quotation_id, line_no),
  FOREIGN KEY (tenant_id, quotation_id) REFERENCES sales_quotation (tenant_id, id),
  FOREIGN KEY (tenant_id, converted_order_id) REFERENCES sales_order (tenant_id, id),
  CHECK ((line_status = 'CONVERTED') = (converted_order_id IS NOT NULL))
);

ALTER TABLE sales_quotation ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_quotation FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_quotation_tenant_access ON sales_quotation AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_quotation_legal_entity_scope ON sales_quotation AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- Lines carry no legal entity of their own, like sales_order_line; they are
-- read only through their quotation, which the legal-entity policy scopes.
ALTER TABLE sales_quotation_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_quotation_line FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_quotation_line_tenant_access ON sales_quotation_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
