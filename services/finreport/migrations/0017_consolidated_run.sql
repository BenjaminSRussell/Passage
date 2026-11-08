-- Consolidated-by-entity runs (FR-15.5) read several ledgers. The run keeps its
-- first ledger in ledger_id (NOT NULL, the single-ledger shape) and lists every
-- ledger and its label in consolidation; legal_entity_ids holds the owning
-- entities. A run is visible to a scoped caller only when the caller's scope
-- covers every one of those entities, never a subset: a partial view of a
-- consolidation would show a sum the caller cannot reproduce.
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS consolidation jsonb;
ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS legal_entity_ids uuid[];

-- An empty list is contained in every scope, so it would show the run to every
-- scoped caller; a consolidated run with an entity-less ledger stores NULL
-- (all-entity callers only) instead.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'statement_run_legal_entity_ids_nonempty') THEN
    ALTER TABLE statement_run ADD CONSTRAINT statement_run_legal_entity_ids_nonempty
      CHECK (legal_entity_ids IS NULL OR cardinality(legal_entity_ids) > 0);
  END IF;
END
$$;

DROP POLICY IF EXISTS statement_run_legal_entity_scope ON statement_run;
CREATE POLICY statement_run_legal_entity_scope ON statement_run AS RESTRICTIVE
  USING (current_setting('passage.legal_entity_scopes', true) = '*'
         OR legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
         OR (legal_entity_ids IS NOT NULL
             AND legal_entity_ids::text[] <@ string_to_array(current_setting('passage.legal_entity_scopes', true), ',')));

DROP POLICY IF EXISTS statement_cell_legal_entity_scope ON statement_cell;
CREATE POLICY statement_cell_legal_entity_scope ON statement_cell AS RESTRICTIVE
  USING (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM statement_run AS scope_parent
      WHERE scope_parent.tenant_id = statement_cell.tenant_id
        AND scope_parent.id = statement_cell.run_id
        AND (scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
             OR (scope_parent.legal_entity_ids IS NOT NULL
                 AND scope_parent.legal_entity_ids::text[] <@ string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
    )
  ) WITH CHECK (
    current_setting('passage.legal_entity_scopes', true) = '*'
    OR EXISTS (
      SELECT 1 FROM statement_run AS scope_parent
      WHERE scope_parent.tenant_id = statement_cell.tenant_id
        AND scope_parent.id = statement_cell.run_id
        AND (scope_parent.legal_entity_id::text = ANY(string_to_array(current_setting('passage.legal_entity_scopes', true), ','))
             OR (scope_parent.legal_entity_ids IS NOT NULL
                 AND scope_parent.legal_entity_ids::text[] <@ string_to_array(current_setting('passage.legal_entity_scopes', true), ',')))
    )
  );
