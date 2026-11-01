-- Fulfillment slice 14: posting settings per legal entity (section 5.28.13).
-- cogs_at_shipment decides whether a packing slip books Dr 5000 (cost of goods
-- sold at shipment) instead of Dr 1405 (delivered not invoiced); asn_enabled
-- decides whether a post publishes the advance ship notice. A post request that
-- names neither takes the setting of the shipment's legal entity. No saved row
-- reads as both false at version 0, which is what posts did before this table.
CREATE TABLE IF NOT EXISTS legal_entity_setting (
  tenant_id uuid NOT NULL,
  legal_entity_id uuid NOT NULL,
  cogs_at_shipment boolean NOT NULL DEFAULT false,
  asn_enabled boolean NOT NULL DEFAULT false,
  version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, legal_entity_id)
);

ALTER TABLE legal_entity_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_entity_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY legal_entity_setting_tenant_access ON legal_entity_setting AS PERMISSIVE
  USING (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  WITH CHECK (tenant_id = current_setting('passage.tenant_id', true)::uuid);
CREATE POLICY legal_entity_setting_legal_entity_scope ON legal_entity_setting AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'passage_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO passage_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON legal_entity_setting TO passage_app';
  END IF;
END
$$;
