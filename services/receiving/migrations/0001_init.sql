CREATE TABLE IF NOT EXISTS receiving_receipt (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  vendor_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  packing_slip text NOT NULL,
  state text NOT NULL CHECK (state IN ('ARRIVED', 'REGISTERED', 'POSTED', 'CORRECTED', 'CANCELLED')),
  receipt_date date NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, vendor_id, packing_slip)
);
CREATE TABLE IF NOT EXISTS receiving_receipt_line (
  tenant_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  line_no int NOT NULL,
  legal_entity_id uuid NOT NULL,
  product_id uuid NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  invoiced_qty numeric(20,6) NOT NULL DEFAULT 0 CHECK (invoiced_qty >= 0 AND invoiced_qty <= qty),
  unit_price numeric(20,6) NOT NULL,
  currency char(3) NOT NULL,
  batch text,
  PRIMARY KEY (tenant_id, receipt_id, line_no),
  FOREIGN KEY (tenant_id, receipt_id) REFERENCES receiving_receipt (tenant_id, id)
);
CREATE TABLE IF NOT EXISTS receiving_serial (
  tenant_id uuid NOT NULL,
  product_id uuid NOT NULL,
  serial text NOT NULL,
  legal_entity_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  line_no int NOT NULL,
  PRIMARY KEY (tenant_id, product_id, serial),
  FOREIGN KEY (tenant_id, receipt_id, line_no) REFERENCES receiving_receipt_line (tenant_id, receipt_id, line_no)
);
CREATE TABLE IF NOT EXISTS receiving_grir_open_item (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  line_no int NOT NULL,
  received_qty numeric(20,6) NOT NULL,
  invoiced_qty numeric(20,6) NOT NULL DEFAULT 0,
  received_amount numeric(20,6) NOT NULL,
  cleared_amount numeric(20,6) NOT NULL DEFAULT 0,
  currency char(3) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, receipt_id, line_no),
  FOREIGN KEY (tenant_id, receipt_id, line_no) REFERENCES receiving_receipt_line (tenant_id, receipt_id, line_no)
);
CREATE TABLE IF NOT EXISTS idempotency_keys (
  tenant_id uuid NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_status int,
  response_body jsonb,
  locked_until timestamptz,
  expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, key)
);
CREATE TABLE IF NOT EXISTS outbox (
  id text PRIMARY KEY,
  tenant_id uuid NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id text NOT NULL,
  type text NOT NULL,
  partition_key text,
  payload jsonb NOT NULL,
  headers jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS consumer_inbox (
  tenant_id uuid NOT NULL,
  consumer text NOT NULL,
  event_id text NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, consumer, event_id)
);
DO $$
DECLARE
  t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['receiving_receipt', 'receiving_receipt_line', 'receiving_serial', 'receiving_grir_open_item', 'idempotency_keys', 'outbox', 'consumer_inbox'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I AS PERMISSIVE USING (tenant_id = current_setting(''passage.tenant_id'', true)::uuid) WITH CHECK (tenant_id = current_setting(''passage.tenant_id'', true)::uuid)', t || '_tenant_access', t);
  END LOOP;
  FOREACH t IN ARRAY ARRAY['receiving_receipt', 'receiving_receipt_line', 'receiving_serial', 'receiving_grir_open_item'] LOOP
    EXECUTE format('CREATE POLICY %I ON %I AS RESTRICTIVE USING (current_setting(''passage.legal_entity_scopes'', true) = ''*'' OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))', t || '_legal_entity_scope', t);
  END LOOP;
END
$$;
