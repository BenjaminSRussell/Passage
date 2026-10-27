-- Wave 16 slice 5: ownership dimension and vendor consignment (FR-16.17, BR-16.14),
-- the segment dimension on reservations (FR-16.20, BR-16.17). Additive: widens two
-- CHECK lists, adds a column and two tables.

ALTER TABLE inventory_journal DROP CONSTRAINT IF EXISTS inventory_journal_journal_type_check;
ALTER TABLE inventory_journal ADD CONSTRAINT inventory_journal_journal_type_check
  CHECK (journal_type IN ('MOVEMENT', 'ADJUSTMENT', 'OPENING_BALANCE', 'COUNT', 'OWNERSHIP_CHANGE'));

-- CONSIGNMENT_LINE origins are vendor-owned receipts of a replenishment order line
-- (reference_number holds the order id). OWNERSHIP_CHANGE origins are the receipt
-- legs of an ownership-change journal line: the issue leg keeps INVENT_JOURNAL_LINE,
-- and the origin key (source_type, source_id) stays unique per leg.
ALTER TABLE inventory_transaction_origin DROP CONSTRAINT IF EXISTS inventory_transaction_origin_source_type_check;
ALTER TABLE inventory_transaction_origin ADD CONSTRAINT inventory_transaction_origin_source_type_check
  CHECK (source_type IN ('INVENT_JOURNAL_LINE', 'PURCH_LINE', 'RECEIPT_LINE', 'SALES_LINE', 'PROD_LINE', 'COUNT_LINE', 'TRANSFER_LINE',
                         'OPENING', 'BLOCKING', 'CONSIGNMENT_LINE', 'OWNERSHIP_CHANGE'));

-- The segment of the demand a reservation serves ('' = unsegmented), kept for the
-- replay check of its Idempotency-Key and shown in the inquiry.
ALTER TABLE inventory_reservation ADD COLUMN IF NOT EXISTS demand_segment text NOT NULL DEFAULT ''
  CHECK (char_length(demand_segment) <= 60);

-- Vendor replenishment order (D365 ConsignmentReplenishmentOrder): vendor-owned stock
-- received as quantity only. Its lines carry the consignment price in currency.
CREATE TABLE IF NOT EXISTS consignment_order (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  id uuid NOT NULL,
  number text NOT NULL,
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  vendor_id text NOT NULL CHECK (char_length(vendor_id) BETWEEN 1 AND 60),
  site text NOT NULL CHECK (char_length(site) BETWEEN 1 AND 60),
  warehouse text NOT NULL CHECK (char_length(warehouse) BETWEEN 1 AND 60),
  requested_date date NOT NULL,
  currency char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  status text NOT NULL CHECK (status IN ('CREATED', 'PARTIALLY_RECEIVED', 'RECEIVED')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, number)
);
CREATE INDEX IF NOT EXISTS consignment_order_vendor_idx ON consignment_order (tenant_id, vendor_id, status);
ALTER TABLE consignment_order ENABLE ROW LEVEL SECURITY;
ALTER TABLE consignment_order FORCE ROW LEVEL SECURITY;
CREATE POLICY consignment_order_tenant_access ON consignment_order AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY consignment_order_legal_entity_scope ON consignment_order AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

CREATE TABLE IF NOT EXISTS consignment_order_line (
  tenant_id uuid NOT NULL,
  consignment_order_id uuid NOT NULL,
  id uuid NOT NULL,
  line_no integer NOT NULL CHECK (line_no > 0),
  product_id uuid NOT NULL,
  dimension_set_hash bytea NOT NULL,
  unit text NOT NULL,
  qty numeric(28,6) NOT NULL CHECK (qty > 0),
  received_qty numeric(28,6) NOT NULL DEFAULT 0,
  price numeric(20,6) NOT NULL DEFAULT 0 CHECK (price >= 0),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, consignment_order_id, line_no),
  FOREIGN KEY (tenant_id, consignment_order_id) REFERENCES consignment_order (tenant_id, id),
  FOREIGN KEY (tenant_id, dimension_set_hash) REFERENCES dimension_set (tenant_id, hash),
  CHECK (received_qty >= 0 AND received_qty <= qty)
);
ALTER TABLE consignment_order_line ENABLE ROW LEVEL SECURITY;
ALTER TABLE consignment_order_line FORCE ROW LEVEL SECURITY;
CREATE POLICY consignment_order_line_tenant_access ON consignment_order_line AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON consignment_order, consignment_order_line TO passage_app';
  END IF;
END
$$;
