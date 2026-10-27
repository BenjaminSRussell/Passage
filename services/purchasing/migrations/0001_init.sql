-- Wave 17 purchasing slice 1: requisitions, purchase orders, trade agreements.
-- Every table carries tenant_id first and RLS (tenant_access + legal_entity_scope
-- where legal_entity_id exists). Money is numeric(20,6) + currency char(3).

CREATE SEQUENCE IF NOT EXISTS purchase_requisition_number_seq;
CREATE SEQUENCE IF NOT EXISTS purchase_order_number_seq;

CREATE TABLE IF NOT EXISTS purchase_requisition (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  operating_unit_id uuid,
  requester_id uuid NOT NULL,
  status text NOT NULL CHECK (status IN ('DRAFT', 'IN_REVIEW', 'APPROVED', 'REJECTED', 'CLOSED', 'CANCELLED')),
  currency char(3) NOT NULL,
  total numeric(20,6) NOT NULL DEFAULT 0 CHECK (total >= 0),
  approver_chain text[] NOT NULL DEFAULT '{}',
  policy_version text,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE INDEX IF NOT EXISTS purchase_requisition_requester_status ON purchase_requisition (tenant_id, requester_id, status);

CREATE TABLE IF NOT EXISTS purchase_requisition_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  requisition_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid,
  description text NOT NULL DEFAULT '',
  category text NOT NULL DEFAULT '',
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  unit text NOT NULL,
  estimated_unit_price numeric(20,6) NOT NULL DEFAULT 0 CHECK (estimated_unit_price >= 0),
  amount numeric(20,6) NOT NULL DEFAULT 0 CHECK (amount >= 0),
  requested_date date NOT NULL,
  site_id uuid,
  preferred_vendor_id uuid,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CONSOLIDATED', 'CANCELLED')),
  PRIMARY KEY (tenant_id, requisition_id, line_no),
  -- FR-17.3: PO lines keep requisition_line_id for commitment tracking.
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, requisition_id) REFERENCES purchase_requisition (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS purchase_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  site_id uuid,
  warehouse_id uuid,
  currency char(3) NOT NULL,
  approval_status text NOT NULL CHECK (approval_status IN ('DRAFT', 'IN_REVIEW', 'APPROVED', 'REJECTED', 'CONFIRMED', 'FINALIZED', 'CANCELLED')),
  document_status text NOT NULL CHECK (document_status IN ('NONE', 'RECEIVED_PARTIAL', 'RECEIVED', 'INVOICED_PARTIAL', 'INVOICED')),
  version_no integer NOT NULL DEFAULT 0 CHECK (version_no >= 0),
  change_management boolean NOT NULL DEFAULT true,
  subtotal numeric(20,6) NOT NULL DEFAULT 0,
  discount_total numeric(20,6) NOT NULL DEFAULT 0,
  charges_total numeric(20,6) NOT NULL DEFAULT 0,
  tax_total numeric(20,6) NOT NULL DEFAULT 0,
  total numeric(20,6) NOT NULL DEFAULT 0,
  row_version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, number)
);
CREATE INDEX IF NOT EXISTS purchase_order_vendor_status ON purchase_order (tenant_id, vendor_id, approval_status);
CREATE INDEX IF NOT EXISTS purchase_requisition_status ON purchase_requisition (tenant_id, status, id);
CREATE INDEX IF NOT EXISTS purchase_order_status ON purchase_order (tenant_id, approval_status, id);

-- FR-17.11 / AC-17.5: header total identity, checked at commit so that every
-- statement in the transaction can update lines and header in any order.
CREATE OR REPLACE FUNCTION purchase_order_total_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.total <> NEW.subtotal - NEW.discount_total + NEW.charges_total + NEW.tax_total THEN
    RAISE EXCEPTION 'PRC-PO-TOTAL: total identity violated for purchase order %', NEW.id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER purchase_order_total_identity
  AFTER INSERT OR UPDATE ON purchase_order
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION purchase_order_total_identity();

CREATE TABLE IF NOT EXISTS purchase_order_line (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid,
  description text NOT NULL DEFAULT '',
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  unit text NOT NULL,
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  price_unit numeric(20,6) NOT NULL DEFAULT 1 CHECK (price_unit > 0),
  discount_pct numeric(9,6) NOT NULL DEFAULT 0 CHECK (discount_pct >= 0 AND discount_pct <= 100),
  gross_amount numeric(20,6) NOT NULL,
  discount_amount numeric(20,6) NOT NULL DEFAULT 0,
  line_amount numeric(20,6) NOT NULL,
  tax_amount numeric(20,6) NOT NULL DEFAULT 0,
  delivery_date date NOT NULL,
  price_source text NOT NULL,
  requisition_line_id uuid,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RECEIVED', 'INVOICED', 'CANCELLED')),
  PRIMARY KEY (tenant_id, purchase_order_id, line_no),
  FOREIGN KEY (tenant_id, purchase_order_id) REFERENCES purchase_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS purchase_order_charge (
  tenant_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  seq integer NOT NULL CHECK (seq > 0),
  calc text NOT NULL CHECK (calc IN ('FIXED', 'PER_UNIT', 'PERCENT')),
  value numeric(20,6) NOT NULL,
  amount numeric(20,6) NOT NULL,
  PRIMARY KEY (tenant_id, purchase_order_id, seq),
  FOREIGN KEY (tenant_id, purchase_order_id) REFERENCES purchase_order (tenant_id, id)
);

CREATE TABLE IF NOT EXISTS trade_agreement (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  relation text NOT NULL CHECK (relation IN ('PRICE', 'LINE_DISC', 'MULTILINE_DISC', 'TOTAL_DISC')),
  account_code text NOT NULL CHECK (account_code IN ('VENDOR', 'GROUP', 'ALL')),
  account_id uuid,
  item_code text NOT NULL CHECK (item_code IN ('PRODUCT', 'GROUP', 'ALL')),
  item_id uuid,
  currency char(3) NOT NULL,
  unit text NOT NULL,
  from_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (from_qty >= 0),
  to_qty numeric(20,6),
  price numeric(20,6) NOT NULL CHECK (price >= 0),
  price_unit numeric(20,6) NOT NULL DEFAULT 1 CHECK (price_unit > 0),
  valid_from date NOT NULL,
  valid_to date,
  consignment boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('DRAFT', 'ACTIVE', 'EXPIRED')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (valid_to IS NULL OR valid_to >= valid_from),
  CHECK (to_qty IS NULL OR to_qty > from_qty)
);
CREATE INDEX IF NOT EXISTS trade_agreement_lookup ON trade_agreement (tenant_id, relation, account_code, account_id, item_code, item_id, valid_from);

CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  resource_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);

-- Row-level security: tenant_access (permissive) everywhere; legal_entity_scope
-- (restrictive) on tables that carry legal_entity_id.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['purchase_requisition', 'purchase_requisition_line', 'purchase_order', 'purchase_order_line',
                           'purchase_order_charge', 'trade_agreement', 'outbox', 'idempotency_keys']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)',
                   t || '_tenant_access', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['purchase_requisition', 'purchase_order']
  LOOP
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
                   t || '_legal_entity_scope', t);
  END LOOP;
END $$;
