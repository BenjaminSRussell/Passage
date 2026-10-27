CREATE TABLE IF NOT EXISTS sales_number_sequence (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  sequence_id text NOT NULL,
  next_value bigint NOT NULL DEFAULT 1,
  PRIMARY KEY (tenant_id, legal_entity_id, sequence_id)
);

CREATE TABLE IF NOT EXISTS sales_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_number text NOT NULL,
  customer_id uuid NOT NULL,
  invoice_account_id uuid NOT NULL,
  order_type text NOT NULL DEFAULT 'ORDER' CHECK (order_type IN ('JOURNAL', 'ORDER', 'SUBSCRIPTION', 'RETURNED')),
  status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'OPEN', 'CONFIRMED', 'PARTIALLY_SHIPPED', 'SHIPPED', 'INVOICED', 'CLOSED', 'CANCELLED')),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  requested_ship_date date NOT NULL,
  net_total numeric(19,4) NOT NULL DEFAULT 0,
  credit_check_result jsonb,
  channel text NOT NULL DEFAULT 'UI',
  external_ref text,
  idempotency_key text,
  request_hash text NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, order_number),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, channel, external_ref)
);
CREATE INDEX IF NOT EXISTS sales_order_customer ON sales_order (tenant_id, legal_entity_id, customer_id);
CREATE INDEX IF NOT EXISTS sales_order_recent ON sales_order (tenant_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS sales_order_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  order_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid NOT NULL,
  quantity numeric(20,6) NOT NULL CHECK (quantity > 0),
  unit text NOT NULL,
  site_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  disc_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (disc_pct BETWEEN 0 AND 100),
  line_net numeric(19,4) NOT NULL,
  requested_ship_date date NOT NULL,
  confirmed_ship_date date,
  line_status text NOT NULL DEFAULT 'OPEN' CHECK (line_status IN ('OPEN', 'RESERVED', 'PICKED', 'DELIVERED', 'INVOICED', 'CANCELLED')),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, order_id, line_no),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS order_hold (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  hold_type text NOT NULL CHECK (hold_type IN ('CREDIT', 'FRAUD', 'MANUAL', 'PRICE_REVIEW')),
  reason_code text NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'RELEASED')),
  order_amount numeric(19,4) NOT NULL,
  applied_by text NOT NULL,
  released_by text,
  applied_at timestamptz NOT NULL DEFAULT now(),
  released_at timestamptz,
  released_until timestamptz,
  PRIMARY KEY (tenant_id, id),
  FOREIGN KEY (tenant_id, order_id) REFERENCES sales_order (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS order_hold_order ON order_hold (tenant_id, order_id);
CREATE INDEX IF NOT EXISTS order_hold_active ON order_hold (tenant_id, legal_entity_id) WHERE status = 'ACTIVE';

CREATE TABLE IF NOT EXISTS credit_profile (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  credit_limit numeric(19,4) NOT NULL CHECK (credit_limit >= 0),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  limit_type text NOT NULL DEFAULT 'BALANCE' CHECK (limit_type IN ('NONE', 'BALANCE', 'BALANCE_PACKING_SLIP', 'BALANCE_ALL')),
  tolerance_pct numeric(9,6) NOT NULL DEFAULT 5 CHECK (tolerance_pct >= 0),
  tolerance_amount numeric(19,4) NOT NULL DEFAULT 1000 CHECK (tolerance_amount >= 0),
  oldest_item_days_max integer NOT NULL DEFAULT 30,
  max_dunning_level integer NOT NULL DEFAULT 3,
  review_date date,
  customer_since date,
  mandatory_check boolean NOT NULL DEFAULT false,
  exposure jsonb NOT NULL DEFAULT '{}'::jsonb,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, customer_id)
);

ALTER TABLE sales_number_sequence ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_number_sequence FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_number_sequence_tenant_access ON sales_number_sequence AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE sales_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_order FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_order_tenant_access ON sales_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY sales_order_legal_entity_scope ON sales_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE sales_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE sales_order_line FORCE ROW LEVEL SECURITY;
CREATE POLICY sales_order_line_tenant_access ON sales_order_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

ALTER TABLE order_hold ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_hold FORCE ROW LEVEL SECURITY;
CREATE POLICY order_hold_tenant_access ON order_hold AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY order_hold_legal_entity_scope ON order_hold AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE credit_profile ENABLE ROW LEVEL SECURITY;
ALTER TABLE credit_profile FORCE ROW LEVEL SECURITY;
CREATE POLICY credit_profile_tenant_access ON credit_profile AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY credit_profile_legal_entity_scope ON credit_profile AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));
