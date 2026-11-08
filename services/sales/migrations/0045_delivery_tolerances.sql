-- Wave 27 slice 39: delivery tolerances and the partial delivery policy per
-- product and customer (BR-27.9, BR-28.7, FR-27.6, PRD §5.27.13
-- overdelivery_pct / underdelivery_pct / partial_delivery_default). A rule
-- names a product and a customer or (customer NULL) every customer of the
-- legal entity; a customer's own rule replaces the all-customers rule. The
-- percentages are of the ordered quantity, served by the resolve call for the
-- shipping check (BR-28.7); fulfillment still keeps its own over-delivery table.

CREATE TABLE IF NOT EXISTS delivery_tolerance (
  tenant_id uuid NOT NULL,
  id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  product_id uuid NOT NULL,
  customer_id uuid,
  overdelivery_pct numeric(5,2) NOT NULL DEFAULT 0 CHECK (overdelivery_pct BETWEEN 0 AND 100),
  underdelivery_pct numeric(5,2) NOT NULL DEFAULT 0 CHECK (underdelivery_pct BETWEEN 0 AND 100),
  partial_delivery text NOT NULL DEFAULT 'ALLOWED' CHECK (partial_delivery IN ('ALLOWED', 'COMPLETE_ONLY', 'ONE_SHIPMENT')),
  version int NOT NULL DEFAULT 1,
  idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
  request_hash text NOT NULL,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key)
);
-- One rule per (product, customer or all customers) in a legal entity.
CREATE UNIQUE INDEX IF NOT EXISTS delivery_tolerance_unique ON delivery_tolerance
  (tenant_id, legal_entity_id, product_id, coalesce(customer_id, '00000000-0000-0000-0000-000000000000'::uuid));

ALTER TABLE delivery_tolerance ENABLE ROW LEVEL SECURITY;
ALTER TABLE delivery_tolerance FORCE ROW LEVEL SECURITY;
CREATE POLICY delivery_tolerance_tenant_access ON delivery_tolerance AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY delivery_tolerance_legal_entity_scope ON delivery_tolerance AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

-- The store inserts, updates by version and deletes a rule.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON delivery_tolerance TO passage_app';
  END IF;
END
$$;
