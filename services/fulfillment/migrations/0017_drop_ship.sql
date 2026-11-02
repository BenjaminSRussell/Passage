-- Fulfillment: drop-ship (FR-28.7, AC-28.5). A drop-ship line is fulfilled by a
-- vendor straight to the customer, linked to the purchase order (wave 17). The
-- record is OPEN until the vendor confirms the shipment; the confirmation books
-- Dr 5000 / Cr 2110 at the purchase order cost, with no inventory transaction,
-- and stores the journal. One active drop-ship per order line, and the PO cost
-- is positive. DS- numbers are gap-free per legal entity.
CREATE TABLE IF NOT EXISTS drop_ship (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  drop_ship_number text NOT NULL,
  legal_entity_id uuid NOT NULL,
  order_id uuid NOT NULL,
  order_line_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  product_id uuid NOT NULL,
  unit text NOT NULL,
  qty numeric(20,6) NOT NULL CHECK (qty > 0),
  vendor_id uuid NOT NULL,
  purchase_order_id uuid NOT NULL,
  purchase_order_line_id uuid NOT NULL,
  unit_cost numeric(20,6) NOT NULL CHECK (unit_cost > 0),
  currency char(3) NOT NULL,
  status text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'CONFIRMED', 'CANCELLED')),
  vendor_shipment_ref text,
  shipped_on date,
  posting_date date,
  journal jsonb NOT NULL DEFAULT '[]'::jsonb,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, legal_entity_id, drop_ship_number),
  CHECK ((status = 'CONFIRMED') = (shipped_on IS NOT NULL AND posting_date IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS drop_ship_order_line_active ON drop_ship (tenant_id, order_line_id) WHERE status <> 'CANCELLED';
CREATE INDEX IF NOT EXISTS drop_ship_order_idx ON drop_ship (tenant_id, order_id);
CREATE INDEX IF NOT EXISTS drop_ship_vendor_status_idx ON drop_ship (tenant_id, vendor_id, status);

ALTER TABLE drop_ship ENABLE ROW LEVEL SECURITY;
ALTER TABLE drop_ship FORCE ROW LEVEL SECURITY;
CREATE POLICY drop_ship_tenant_access ON drop_ship AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY drop_ship_legal_entity_scope ON drop_ship AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON drop_ship TO passage_app';
  END IF;
END
$$;
