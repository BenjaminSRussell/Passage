-- Wave 27 slice 3: product listings and exclusions (FR-27.15, BR-27.13), the
-- listing overrides logged on an order, and sales agreements whose releases
-- create orders and decrement the commitment atomically (FR-27.10, BR-27.9).

-- customer_group stands in for the w7 customer group until the party lookup
-- exists; listing_overrides logs every denied line accepted with
-- SalesOrderApprove and a reason (AC-27.11).
ALTER TABLE sales_order ADD COLUMN IF NOT EXISTS customer_group text CHECK (customer_group IS NULL OR length(customer_group) BETWEEN 1 AND 80);
ALTER TABLE sales_order ADD COLUMN IF NOT EXISTS listing_overrides jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS product_listing (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  scope_type text NOT NULL CHECK (scope_type IN ('CUSTOMER', 'GROUP')),
  scope_key text NOT NULL CHECK (length(scope_key) BETWEEN 1 AND 80),
  product_id uuid NOT NULL,
  listing_type text NOT NULL CHECK (listing_type IN ('ALLOW', 'DENY')),
  valid_from date,
  valid_to date,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from <= valid_to)
);
CREATE INDEX IF NOT EXISTS product_listing_product ON product_listing (tenant_id, legal_entity_id, product_id);

CREATE TABLE IF NOT EXISTS sales_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_number text NOT NULL,
  customer_id uuid NOT NULL,
  invoice_account_id uuid NOT NULL,
  product_id uuid NOT NULL,
  unit text NOT NULL CHECK (length(unit) BETWEEN 1 AND 10),
  unit_price numeric(19,4) NOT NULL CHECK (unit_price >= 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  commitment_qty numeric(20,6) NOT NULL CHECK (commitment_qty > 0),
  released_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (released_qty >= 0),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'EXHAUSTED')),
  valid_from date,
  valid_to date,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  created_by text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, legal_entity_id, agreement_number),
  CHECK (released_qty <= commitment_qty),
  CHECK (valid_from IS NULL OR valid_to IS NULL OR valid_from <= valid_to)
);
CREATE INDEX IF NOT EXISTS sales_agreement_customer ON sales_agreement (tenant_id, legal_entity_id, customer_id);

CREATE TABLE IF NOT EXISTS agreement_release (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  agreement_id uuid NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  sales_order_id uuid NOT NULL,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  actor text NOT NULL,
  released_at timestamptz NOT NULL,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  FOREIGN KEY (tenant_id, agreement_id) REFERENCES sales_agreement (tenant_id, id),
  FOREIGN KEY (tenant_id, sales_order_id) REFERENCES sales_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS agreement_release_agreement ON agreement_release (tenant_id, agreement_id, released_at);

ALTER TABLE product_listing ENABLE ROW LEVEL SECURITY;
ALTER TABLE product_listing FORCE ROW LEVEL SECURITY;
CREATE POLICY product_listing_tenant_access ON product_listing AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY product_listing_legal_entity_scope ON product_listing AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE sales_agreement ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_agreement FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_agreement_tenant_access ON sales_agreement AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_agreement_legal_entity_scope ON sales_agreement AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE agreement_release ENABLE ROW LEVEL SECURITY;
ALTER TABLE agreement_release FORCE ROW LEVEL SECURITY;
CREATE POLICY agreement_release_tenant_access ON agreement_release AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY agreement_release_legal_entity_scope ON agreement_release AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
