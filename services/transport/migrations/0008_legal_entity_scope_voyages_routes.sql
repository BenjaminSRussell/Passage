-- Landed-cost voyages and route plans carry the owning legal entity (#292). The
-- column is nullable so existing rows stay valid: a NULL entity matches no
-- scoped caller and is visible only to all-entity callers until it is assigned.
--
-- Per-table decisions:
--   voyage         entity-specific root. Its legs, containers, folios, cost lines
--                  and receipt lines are JSON documents inside the row, so they
--                  follow the voyage with no policy of their own. Created under
--                  httpx.OwnerLegalEntity; goods-in-transit orders opened on a
--                  voyage take the voyage's entity.
--   route_plan     entity-specific root (a plan is built for one entity's loads
--                  and fleet). Created under httpx.OwnerLegalEntity. The request
--                  hash is the idempotency key per entity, so two entities that
--                  send the same request each get their own plan.
--   rate_card      stays tenant-level: carrier rates are negotiated per tenant
--                  and shared by every entity when rating.
ALTER TABLE voyage ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE route_plan ADD COLUMN IF NOT EXISTS legal_entity_id uuid;

DO $$
DECLARE
  scoped_table text;
BEGIN
  FOREACH scoped_table IN ARRAY ARRAY['voyage', 'route_plan']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE
         USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
                OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
      scoped_table || '_legal_entity_scope', scoped_table
    );
  END LOOP;
END $$;

ALTER TABLE route_plan DROP CONSTRAINT IF EXISTS route_plan_tenant_id_request_hash_key;
CREATE UNIQUE INDEX IF NOT EXISTS route_plan_entity_request_hash ON route_plan (tenant_id, legal_entity_id, request_hash);
