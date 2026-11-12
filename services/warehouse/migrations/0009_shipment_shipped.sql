-- #35 (35-1): the warehouse's confirmation that an outbound shipment left the dock without a packing slip posted for it.
-- One row per fulfillment shipment; shipment.shipped is enqueued in the same transaction so the EDI engine sends the 856.

CREATE TABLE IF NOT EXISTS shipment_shipped (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  shipment_id uuid NOT NULL,
  warehouse_id uuid NOT NULL,
  carrier_code text NOT NULL DEFAULT '' CHECK (carrier_code = '' OR carrier_code ~ '^[A-Z0-9]{2,4}$'),
  shipped_at timestamptz NOT NULL,
  ssccs text[] NOT NULL DEFAULT '{}',
  confirmed_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, shipment_id)
);

ALTER TABLE shipment_shipped ENABLE ROW LEVEL SECURITY;
ALTER TABLE shipment_shipped FORCE ROW LEVEL SECURITY;
CREATE POLICY shipment_shipped_tenant_access ON shipment_shipped AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY shipment_shipped_legal_entity_scope ON shipment_shipped AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
  WITH CHECK (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE 'GRANT SELECT, INSERT ON shipment_shipped TO passage_app';
  END IF;
END
$$;
