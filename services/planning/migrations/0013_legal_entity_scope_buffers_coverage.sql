-- Firming batches, item coverages and DDMRP buffers carry the owning legal
-- entity (#292). The column is nullable so existing rows stay valid: a NULL
-- entity matches no scoped caller and is visible only to all-entity callers
-- until it is assigned.
--
-- Per-table decisions:
--   firming_batch   entity-specific root. Its response document (the target
--                   orders with site, vendor and quantities) is inside the row.
--                   The entity is the one of the planned orders' plan version;
--                   a request that mixes entities is refused.
--   item_coverage   entity-specific root (an item planned at a site). Created
--                   under httpx.OwnerLegalEntity; the site register lives in the
--                   org service, so the entity cannot be derived from the site.
--   ddmrp_buffer    entity-specific root (one buffer per item and site scope).
--                   Created under httpx.OwnerLegalEntity by the recalculation.
--   coverage_group  stays tenant-level: planning parameters (fences, grouping)
--   buffer_profile  stay tenant-level: shared setup that item coverages and
--                   buffers of every entity of the tenant refer to by id.
ALTER TABLE firming_batch ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE item_coverage ADD COLUMN IF NOT EXISTS legal_entity_id uuid;
ALTER TABLE ddmrp_buffer ADD COLUMN IF NOT EXISTS legal_entity_id uuid;

DO $$
DECLARE
  scoped_table text;
BEGIN
  FOREACH scoped_table IN ARRAY ARRAY['firming_batch', 'item_coverage', 'ddmrp_buffer']
  LOOP
    EXECUTE format(
      'CREATE POLICY %I ON %I AS RESTRICTIVE
         USING (current_setting(''passage.legal_entity_scopes'', true) = ''*''
                OR legal_entity_id::text = ANY(string_to_array(current_setting(''passage.legal_entity_scopes'', true), '','')))',
      scoped_table || '_legal_entity_scope', scoped_table
    );
  END LOOP;
END $$;
