-- Wave 30 AC3: return orders (RMA). One RMA drives authorize → receive →
-- inspect → disposition → credit (FR-30.11, BR-30.3, BR-30.6). CREDIT_ONLY and
-- RETURN_TO_STOCK are the dispositions this slice posts; the rest stay later.
CREATE TABLE IF NOT EXISTS return_order (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  rma_number text NOT NULL,
  customer_id uuid NOT NULL,
  original_invoice_id uuid,
  return_reason_code text NOT NULL CHECK (char_length(return_reason_code) BETWEEN 1 AND 40),
  status text NOT NULL CHECK (status IN (
    'REQUESTED', 'AUTHORIZED', 'RECEIVED', 'INSPECTED', 'DISPOSITIONED', 'CREDITED', 'CLOSED', 'REJECTED', 'EXPIRED')),
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  expires_at date,
  version int NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  gross numeric(20,6) NOT NULL DEFAULT 0,
  restocking numeric(20,6) NOT NULL DEFAULT 0,
  net numeric(20,6) NOT NULL DEFAULT 0,
  credit_note_id text,
  journal_posting_id text,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, legal_entity_id, rma_number)
);

CREATE TABLE IF NOT EXISTS return_order_line (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  return_order_id uuid NOT NULL,
  line_no int NOT NULL CHECK (line_no >= 1),
  product_id uuid NOT NULL,
  site_id uuid,
  warehouse_id uuid,
  quantity_expected numeric(20,6) NOT NULL CHECK (quantity_expected > 0),
  quantity_received numeric(20,6) NOT NULL DEFAULT 0 CHECK (quantity_received >= 0 AND quantity_received <= quantity_expected),
  unit text NOT NULL DEFAULT 'EA',
  unit_price numeric(20,6) NOT NULL CHECK (unit_price >= 0),
  disposition_code text CHECK (disposition_code IS NULL OR disposition_code IN ('CREDIT_ONLY', 'RETURN_TO_STOCK')),
  return_cost_method text NOT NULL DEFAULT 'ORIGINAL_COST'
    CHECK (return_cost_method IN ('ORIGINAL_COST', 'CURRENT_COST', 'ZERO')),
  restocking_pct numeric(20,6),
  restocking_amount numeric(20,6) NOT NULL DEFAULT 0,
  inventory_receipt_id text,
  credit_note_line_id text,
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, return_order_id, line_no),
  FOREIGN KEY (tenant_id, return_order_id) REFERENCES return_order (tenant_id, id)
);

ALTER TABLE return_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE return_order FORCE ROW LEVEL SECURITY;
CREATE POLICY return_order_tenant_access ON return_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY return_order_legal_entity_scope ON return_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

ALTER TABLE return_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE return_order_line FORCE ROW LEVEL SECURITY;
CREATE POLICY return_order_line_tenant_access ON return_order_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON return_order, return_order_line TO passage_app';
  END IF;
END
$$;
