-- Field service truck stock (wave 45, FR-45.9, BR-45.7): a technician's truck is a
-- warehouse of its own. The completion of a service work order issues the consumed parts from
-- it (origin SERVICE_LINE, reference = the work order number), and a truck whose stock falls
-- below its minimum gets a draft transfer order from its source warehouse up to its maximum.

ALTER TABLE inventory_transaction_origin DROP CONSTRAINT IF EXISTS inventory_transaction_origin_source_type_check;
ALTER TABLE inventory_transaction_origin ADD CONSTRAINT inventory_transaction_origin_source_type_check
  CHECK (source_type IN ('INVENT_JOURNAL_LINE', 'PURCH_LINE', 'RECEIPT_LINE', 'SALES_LINE', 'PROD_LINE', 'COUNT_LINE', 'TRANSFER_LINE',
                         'OPENING', 'BLOCKING', 'CONSIGNMENT_LINE', 'OWNERSHIP_CHANGE', 'TRANSFER_IN', 'SERVICE_LINE'));

-- Min and max per truck and product, and where the truck is replenished from.
CREATE TABLE IF NOT EXISTS inventory_truck_level (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  site_id text NOT NULL CHECK (char_length(site_id) BETWEEN 1 AND 60),
  warehouse_id text NOT NULL CHECK (char_length(warehouse_id) BETWEEN 1 AND 60),
  product_id uuid NOT NULL,
  unit text NOT NULL CHECK (unit ~ '^[A-Z0-9_-]{1,12}$'),
  min_qty numeric(28,6) NOT NULL CHECK (min_qty >= 0),
  max_qty numeric(28,6) NOT NULL CHECK (max_qty > 0),
  source_site_id text NOT NULL CHECK (char_length(source_site_id) BETWEEN 1 AND 60),
  source_warehouse_id text NOT NULL CHECK (char_length(source_warehouse_id) BETWEEN 1 AND 60),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id, site_id, warehouse_id, product_id),
  CHECK (max_qty >= min_qty)
);
ALTER TABLE inventory_truck_level ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_truck_level FORCE ROW LEVEL SECURITY;
CREATE POLICY inventory_truck_level_tenant_access ON inventory_truck_level AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY inventory_truck_level_legal_entity_scope ON inventory_truck_level AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON inventory_truck_level TO passage_app';
  END IF;
END
$$;
